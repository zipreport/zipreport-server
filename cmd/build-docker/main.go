// Note: adapted from https://github.com/go-rod/rod/blob/main/lib/docker/main.go
// The .github/workflows/docker.yml uses it as an github action
// and run it like this:
//
//	GITHUB_TOKEN=$TOKEN go run ./cmd/build-docker/ $GITHUB_REF
package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/go-rod/rod/lib/utils"
)

const registry = "ghcr.io/zipreport/zipreport-server"

type archType int

const (
	archAmd archType = iota
	archArm
)

var headSha = strings.TrimSpace(utils.ExecLine(false, "git", "rev-parse", "HEAD"))

func main() {
	event := os.Args[1]

	fmt.Println("Event:", event)

	isMain := regexp.MustCompile(`^refs/heads/master$`).MatchString(event)
	isDev := regexp.MustCompile(`^refs/heads/development$`).MatchString(event)
	m := regexp.MustCompile(`^refs/tags/(v[0-9]+\.[0-9]+\.[0-9]+)$`).FindStringSubmatch(event)
	ver := ""
	if len(m) > 1 {
		ver = m[1]
	}

	at := getArchType()

	if isMain {
		releaseLatest(at)
	} else if isDev {
		releaseDev(at)
	} else if ver != "" {
		releaseWithVer(ver)
	}
}

func releaseLatest(at archType) {
	login()
	build(at)

	utils.Exec("docker push", at.tag())
}

func releaseDev(at archType) {
	login()
	build_dev(at)

	utils.Exec("docker push", at.devTag())
}

func releaseWithVer(ver string) {
	login()

	verImage := registry + ":" + ver
	utils.Exec("docker manifest create", verImage, archAmd.tag(), archArm.tag())
	utils.Exec("docker manifest push", verImage)

	utils.Exec("docker manifest create", registry, archAmd.tag(), archArm.tag())
	utils.Exec("docker manifest push", registry)
}

func description() string {
	return `--label=org.opencontainers.image.description=https://github.com/zipreport/zipreport-server/blob/` + headSha + "/Dockerfile"
}

func build(at archType) {
	utils.Exec("docker build -f=Dockerfile", "--platform", at.platform(), "-t", at.tag(), description(), ".")
}

func build_dev(at archType) {
	utils.Exec("docker build -f=Dockerfile", "--platform", at.platform(), "-t", at.devTag(), description(), ".")
}

func login() {
	cmd := exec.Command("docker", "login", "-u=zipreport-robot", "-p", os.Getenv("GITHUB_TOKEN"), registry)
	out, err := cmd.CombinedOutput()
	utils.E(err)
	utils.E(os.Stdout.Write(out))
}

func getArchType() archType {
	arch := os.Getenv("ARCH")
	switch arch {
	case "arm":
		return archArm
	default:
		return archAmd
	}
}

func (at archType) platform() string {
	switch at {
	case archArm:
		return "linux/arm64"
	default:
		return "linux/amd64"
	}
}

func (at archType) tag() string {
	switch at {
	case archArm:
		return registry + ":arm"
	default:
		return registry + ":amd"
	}
}

func (at archType) devTag() string {
	switch at {
	case archArm:
		return registry + "-dev:arm"
	default:
		return registry + "-dev:amd"
	}
}