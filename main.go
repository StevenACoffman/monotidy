package main

import (
	"context"
	"fmt"
	"github.com/dependabot/gomodules-extracted/cmd/go/_internal_/imports"
	"go/build"
	"os"
	"strings"

	"github.com/dependabot/gomodules-extracted/cmd/go/_internal_/base"
	"github.com/dependabot/gomodules-extracted/cmd/go/_internal_/cfg"
	"github.com/dependabot/gomodules-extracted/cmd/go/_internal_/modload"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
	"io/fs"
	"path/filepath"
)
func main() {
	rootDir, err := os.Getwd()
	fmt.Printf("Root Working Direcotry: %s\n", rootDir)
	if err != nil {
		panic(err)
	}
	goModDirs := findGoModFiles(rootDir)
	for i := range goModDirs {
		goModDir := filepath.Dir(goModDirs[i])
		fmt.Println("Changing to "+goModDir)
		chErr := os.Chdir(goModDir)
		if chErr != nil {
			panic(chErr)
		}
		base.Cwd = goModDir
		newDir, wdErr := os.Getwd()
		if wdErr != nil {
			panic(wdErr)
		}
		fmt.Printf("Current Working Direcotry: %s\n", newDir)
		cmdTidy.Run(context.Background(), cmdTidy, os.Args)
	}

}

// findGoModfiles is pretty stupid
func findGoModFiles(root string) []string {
	var a []string
	err := filepath.WalkDir(root, func(s string, d fs.DirEntry, e error) error {
		if e != nil { return e }
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
	os.Setenv("GOFLAGS","-mod=mod")
	cfg.BuildMod = "mod"
	cfg.BuildModExplicit=true
	//modload.DisallowWriteGoMod()
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
		Tags:                     imports.AnyTags(),
		ResolveMissingImports:    true,
		LoadTests:                true,
		AllowErrors:              true,
		SilenceErrors: true,
		SilenceMissingStdImports: true,
		SilenceUnmatchedWarnings: true,
	}, "all")
	fmt.Println("loaded packages")
	modload.TidyBuildList()
	fmt.Println("tidied buildlist")
	modload.TrimGoSum()
	fmt.Println("trimmed GoSum")
	modload.AllowWriteGoMod()
	fmt.Println("allowed write Go Mod")
	modload.WriteGoMod()

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