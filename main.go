package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"go/build"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	altsemver "github.com/Masterminds/semver/v3"
	"github.com/apex/log"
	logcli "github.com/apex/log/handlers/cli"
	"github.com/dependabot/gomodules-extracted/cmd/go/_internal_/base"
	"github.com/dependabot/gomodules-extracted/cmd/go/_internal_/cfg"
	"github.com/dependabot/gomodules-extracted/cmd/go/_internal_/imports"
	"github.com/dependabot/gomodules-extracted/cmd/go/_internal_/modload"
	"github.com/fatih/color"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

func main() {
	log.SetHandler(logcli.Default)
	var updateMods bool
	flag.BoolVar(&updateMods, "update", false, "discover and update")
	flag.Parse()
	rootDir, err := os.Getwd()
	fmt.Printf("Root Working Directory: %s\n", rootDir)
	if err != nil {
		panic(err)
	}
	goModDirs := findGoModFiles(rootDir)
	for i := range goModDirs {
		goModDir := filepath.Dir(goModDirs[i])
		fmt.Println("Changing to " + goModDir)
		chErr := os.Chdir(goModDir)
		if chErr != nil {
			panic(chErr)
		}
		base.Cwd()
		base.ChangeCwd(goModDir)
		if updateMods {
			modules, dicoverErr := discover()
			if dicoverErr != nil && modules == nil {
				fmt.Println(dicoverErr)
				continue
			}
			fmt.Println("running updates:", len(modules))
			update(modules)
		}
		newDir, wdErr := os.Getwd()
		if wdErr != nil {
			panic(wdErr)
		}
		fmt.Printf("Current Working Directory: %s\n", newDir)
		cmdTidy.Run(context.Background(), cmdTidy, []string{})
	}
}

// findGoModfiles is pretty stupid.
func findGoModFiles(root string) []string {
	var a []string
	err := filepath.WalkDir(root, func(s string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Name() == "go.mod" {
			a = append(a, s)
		}
		return nil
	})
	if err != nil {
		panic(err)
	}
	return a
}

var cmdTidy = &base.Command{
	UsageLine: "go mod tidy [-e] [-v] [-go=version] [-compat=version]",
	Short:     "add missing and remove unused modules",
	Long: `
Tidy makes sure go.mod matches the source code in the module.
It adds any missing modules necessary to build the current module's
packages and dependencies, and it removes unused modules that
don't provide any relevant packages. It also adds any missing entries
to go.sum and removes any unnecessary ones.
The -v flag causes tidy to print information about removed modules
to standard error.
The -e flag causes tidy to attempt to proceed despite errors
encountered while loading packages.
The -go flag causes tidy to update the 'go' directive in the go.mod
file to the given version, which may change which module dependencies
are retained as explicit requirements in the go.mod file.
(Go versions 1.17 and higher retain more requirements in order to
support lazy module loading.)
The -compat flag preserves any additional checksums needed for the
'go' command from the indicated major Go release to successfully load
the module graph, and causes tidy to error out if that version of the
'go' command would load any imported package from a different module
version. By default, tidy acts as if the -compat flag were set to the
version prior to the one indicated by the 'go' directive in the go.mod
file.
See https://golang.org/ref/mod#go-mod-tidy for more about 'go mod tidy'.
	`,
	Run: runTidy,
}

var (
	tidyE      bool          // if true, report errors but proceed anyway.
	tidyGo     goVersionFlag // go version to write to the tidied go.mod file (toggles lazy loading)
	tidyCompat goVersionFlag // go version for which the tidied go.mod and go.sum files should be “compatible”
)

func init() {
	cmdTidy.Flag.BoolVar(&cfg.BuildV, "v", false, "")
	cmdTidy.Flag.BoolVar(&tidyE, "e", false, "")
	cmdTidy.Flag.Var(&tidyGo, "go", "")
	cmdTidy.Flag.Var(&tidyCompat, "compat", "")
	base.AddModCommonFlags(&cmdTidy.Flag)
}

// A goVersionFlag is a flag.Value representing a supported Go version.
//
// (Note that the -go argument to 'go mod edit' is *not* a goVersionFlag.
// It intentionally allows newer-than-supported versions as arguments.)
type goVersionFlag struct {
	v string
}

func (f *goVersionFlag) String() string   { return f.v }
func (f *goVersionFlag) Get() interface{} { return f.v }

func (f *goVersionFlag) Set(s string) error {
	if s != "" {
		latest := LatestGoVersion()
		if !modfile.GoVersionRE.MatchString(s) {
			return fmt.Errorf("expecting a Go version like %q", latest)
		}
		if semver.Compare("v"+s, "v"+latest) > 0 {
			return fmt.Errorf("maximum supported Go version is %s", latest)
		}
	}

	f.v = s
	return nil
}

// runTidy tends to change upstream. A lot. The specific version I looted:
// https://github.com/golang/go/blame/fa6aa872225f8d33a90d936e7a81b64d2cea68e1/src/cmd/go/internal/modcmd/tidy.go#L114
func runTidy(ctx context.Context, _ *base.Command, _ []string) {
	var (
		tidyGo     goVersionFlag // go version to write to the tidied go.mod file (toggles lazy loading)
		tidyCompat goVersionFlag // go version for which the tidied go.mod and go.sum files should be “compatible”
	)
	tidyGo = goVersionFlag{
		v: runtime.Version(),
	}
	tidyCompat = goVersionFlag{
		v: runtime.Version(),
	}

	os.Setenv("GOFLAGS", "-mod=mod")
	cfg.BuildMod = "mod"
	cfg.BuildModExplicit = true
	// modload.DisallowWriteGoMod()
	modload.AllowWriteGoMod()
	// Tidy aims to make 'go test' reproducible for any package in 'all', so we
	// need to include test dependencies. For modules that specify go 1.15 or
	// earlier this is a no-op (because 'all' saturates transitive test
	// dependencies).
	//
	// However, with lazy loading (go 1.16+) 'all' includes only the packages that
	// are transitively imported by the main module, not the test dependencies of
	// those packages. In order to make 'go test' reproducible for the packages
	// that are in 'all' but outside of the main module, we must explicitly
	// request that their test dependencies be included.
	modload.ForceUseModules = true
	modload.RootMode = modload.NeedRoot
	fmt.Println("about to load packages")
	modload.LoadPackages(ctx, modload.PackageOpts{
		GoVersion:                tidyGo.String(),
		Tags:                     imports.AnyTags(),
		Tidy:                     true,
		TidyCompatibleVersion:    tidyCompat.String(),
		VendorModulesInGOROOTSrc: true,
		ResolveMissingImports:    true,
		LoadTests:                true,
		AllowErrors:              true,
		SilenceMissingStdImports: true,
	}, "all")
	fmt.Println("loaded packages")

	modload.AllowWriteGoMod()
	fmt.Println("allowed write Go Mod")
	modload.WriteGoMod(ctx)
}

// LatestGoVersion returns the latest version of the Go language supported by
// this toolchain, like "1.17".
// Not in dependabot/gomodules-extracted/cmd/go/_internal_/modload so Lifted from
// https://github.com/golang/go/blob/891547e2d4bc2a23973e2c9f972ce69b2b48478e/src/cmd/go/internal/modload/init.go#L775
func LatestGoVersion() string {
	tags := build.Default.ReleaseTags
	version := tags[len(tags)-1]
	if !strings.HasPrefix(version, "go") || !modfile.GoVersionRE.MatchString(version[2:]) {
		base.Fatalf("go: internal error: unrecognized default version %q", version)
	}
	return version[2:]
}

type Module struct {
	name string
	from *altsemver.Version
	to   *altsemver.Version
}

func discover() ([]Module, error) {
	log.Info("Discovering modules...")
	args := []string{
		"list",
		"-u",
		"-mod=mod",
		"-f",
		"'{{if (and (not (or .Main .Indirect)) .Update)}}{{.Path}}: {{.Version}} -> {{.Update.Version}}{{end}}'",
		"-m",
		"all",
	}
	list, err := exec.Command("go", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("Error running go command to discover modules: %w", err)
	}
	split := strings.Split(string(list), "\n")
	modules := []Module{}
	re := regexp.MustCompile(`'(.+): (.+) -> (.+)'`)
	for _, x := range split {
		if x != "''" && x != "" {
			matched := re.FindStringSubmatch(x)
			if len(matched) < 4 {
				return nil, fmt.Errorf("Couldn't parse module %s", x)
			}
			name, from, to := matched[1], matched[2], matched[3]
			log.WithFields(log.Fields{
				"name": name,
				"from": from,
				"to":   to,
			}).Debug("Found module")
			fromversion, err := altsemver.NewVersion(from)
			if err != nil {
				return nil, err
			}
			toversion, err := altsemver.NewVersion(to)
			if err != nil {
				return nil, err
			}
			d := Module{
				name: name,
				from: fromversion,
				to:   toversion,
			}
			modules = append(modules, d)
		}
	}
	return modules, nil
}

func padRight(str string, length int) string {
	if len(str) >= length {
		return str
	}
	return str + strings.Repeat(" ", length-len(str))
}

func formatFrom(from *altsemver.Version, length int) string {
	c := color.New(color.FgBlue).SprintFunc()
	return c(padRight(from.String(), length))
}

func formatTo(module Module) string {
	green := color.New(color.FgGreen).SprintFunc()
	var buf bytes.Buffer
	from := module.from
	to := module.to
	same := true
	fmt.Fprintf(&buf, "%d.", to.Major())
	if from.Minor() == to.Minor() {
		fmt.Fprintf(&buf, "%d.", to.Minor())
	} else {
		fmt.Fprintf(&buf, "%s%s", green(to.Minor()), green("."))
		same = false
	}
	if from.Patch() == to.Patch() && same {
		fmt.Fprintf(&buf, "%d", to.Patch())
	} else {
		fmt.Fprintf(&buf, "%s", green(to.Patch()))
		same = false
	}
	if to.Prerelease() != "" {
		if from.Prerelease() == to.Prerelease() && same {
			fmt.Fprintf(&buf, "-%s", to.Prerelease())
		} else {
			fmt.Fprintf(&buf, "-%s", green(to.Prerelease()))
		}
	}
	if to.Metadata() != "" {
		fmt.Fprintf(&buf, "%s%s", green("+"), green(to.Metadata()))
	}
	return buf.String()
}

func formatName(module Module, length int) string {
	c := color.New(color.FgWhite).SprintFunc()
	from := module.from
	to := module.to
	if from.Minor() != to.Minor() {
		c = color.New(color.FgYellow).SprintFunc()
	}
	if from.Patch() != to.Patch() {
		c = color.New(color.FgGreen).SprintFunc()
	}
	if from.Prerelease() != to.Prerelease() {
		c = color.New(color.FgRed).SprintFunc()
	}
	return c(padRight(module.name, length))
}

func update(modules []Module) {
	for _, x := range modules {
		fmt.Fprintf(
			color.Output,
			"Updating %s to version %s...\n",
			formatName(x, len(x.name)),
			formatTo(x),
		)
		out, err := exec.Command("go", "get", x.name).CombinedOutput()
		if err != nil {
			log.WithFields(log.Fields{
				"error": err,
				"name":  x.name,
				"out":   string(out),
			}).Error("Error while updating module")
		}
	}
}
