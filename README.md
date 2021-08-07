### monotidy
go mod tidy, but for multi-module monorepos

### Why?

In a multi-module monorepo, when dependabot updates a shared lib's `go.mod`, the `go.sum` of all the dependent applications in the same repo (if they use a replace directive for the local relative path), then `go mod verify` will fail, breaking the build without manual intervention.

Ideally, when we update a shared library directory, we would then run `go mod tidy` on all the dependent (e.g. application) modules, and commit back the changed `go.sum` files.

This can be done with a dumb little bash script:
```shell
 #!/bin/bash

 # this will update all the go modules, and show you if they go.mod is targetting
 # old go versions. Requires go-mod-upgrade

 # go get -u github.com/oligot/go-mod-upgrade
function is_bin_in_path {
  builtin type -P "$1" &> /dev/null
}

function modupdate {
    for D in */; do
        if [ -f "${D}go.mod" ]; then
            echo -e "\033[32m\xE2\x9c\x93 Updating modules for ${D}\n\n\033[0m"
            rm "./${D}go.sum"
            # cat "./${D}go.mod" | grep 'go 1.' # list go version
            cd "${D}"
            #go get -u all
            # ! is_bin_in_path go-mod-upgrade && go get -u github.com/oligot/go-mod-upgrade
            #is_bin_in_path go-mod-upgrade && go-mod-upgrade
            go mod tidy
            #go mod verify
            # go mod download

            # go vet ./...
            cd ..
        fi
    done
}
```

However, that assumes we have a whole unix environment and Golang dev tools, which bloats the size of any CI container and slows down the whole process.

I want juuuuuust `go mod tidy`, but from a root directory on all go.mod file containing subdirectories, so I can make a static binary to shove in a scratch container.

Luckily, github's dependabot helpfully scapes and copies the go tools internal packages and renames them so they are re-usable, so I can!

Then I can just hook dependabot up to THIS repo, and it will maintain itself. Muhahahahaha!

_insert further mad science here_