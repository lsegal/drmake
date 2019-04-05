package main

import (
	"crypto/sha1"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	flags "github.com/jessevdk/go-flags"
)

const (
	defaultTarget = "default"

	version = "1.0"
)

var (
	opts struct {
		Makefile  string   `short:"f" long:"file" value-name:"FILE" default:"Makefile.phd" description:"The build file to parse targets from"`
		Fresh     bool     `long:"fresh" description:"Run containers in fresh volume (defaults to false)"`
		Host      bool     `long:"host" description:"Mount images to host workspace volume"`
		PrintList bool     `short:"l" long:"list" description:"Print a list of targets"`
		Args      []string `short:"a" long:"arg" value-name:"ARG=value" description:"An argument in the form ARG=value to pass to a target"`
		Version   bool     `long:"version" description:"Show version information"`
	}

	tempdir string
	origdir string

	reFromLine = regexp.MustCompile(`(?i)^FROM\s+(\S+)(?:\s+AS\s+(\S+))?(?:\s+USING\s+(.+)$)?`)
)

type target struct {
	name  string
	image string
	defn  string
	desc  string
	deps  []string

	artifacts map[string]string
}

type targetlist map[string]*target

func (s targetlist) find(name string) *target {
	if s[name] == nil {
		log.Fatal("Unknown target: ", name)
		return nil
	}
	return s[name]
}

func (s *target) String() string {
	return fmt.Sprintf("target %s FROM %s: %s\n%s",
		s.name, s.image, strings.Join(s.deps, " "), s.defn)
}

func (s *target) Run(list targetlist) {
	dfile := s.Dockerfile(list)
	if dfile != "" || !strings.HasPrefix(s.image, "#") {
		args := []string{"build", "--rm", "-t", image() + "/" + s.name}
		buildArgs := []string{}
		for _, arg := range opts.Args {
			buildArgs = append(buildArgs, []string{"--build-arg", arg}...)
		}
		args = append(args, buildArgs...)
		args = append(args, "-")
		cmd := exec.Command("docker", args...)
		cmd.Stdin = strings.NewReader(dfile)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			os.Exit(1)
		}

		cmd = exec.Command("docker", "run", "--rm", "-v", cachevol()+":/root",
			"-v", wsvol()+":/work", "-w", "/work", "-it", image()+"/"+s.name)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			os.Exit(1)
		}
	}

	if !opts.Host && len(s.artifacts) > 0 {
		uid := os.Getuid()
		gid := os.Getgid()
		for src, dst := range s.artifacts {
			finaldst := filepath.Join(origdir, filepath.FromSlash(dst))
			log.Printf("Copying artifact %s to %s\n", src, finaldst)
			copyVolAll("/work/"+src, "/srv/"+dst)
			filepath.Walk(finaldst, func(name string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				return os.Chown(name, uid, gid)
			})
		}
	}
}

func (s *target) Dockerfile(list targetlist) string {
	preface := "FROM " + s.image
	if strings.HasPrefix(s.image, "&") {
		preface = s.dockerfileFromPath(filepath.Join(".drmake", "targets", s.image[1:]), list)
	} else if strings.HasPrefix(s.image, "#") {
		if s.image[1:] == s.name {
			return ""
		}
		pretarget := list.find(s.image[1:])
		preface = strings.Trim(pretarget.Dockerfile(list), " \r\n")
	} else if strings.HasPrefix(s.image, "./") {
		preface = s.dockerfileFromPath(s.image[2:], list)
	}
	os.Chdir(tempdir)
	return strings.Join([]string{preface, s.defn}, "\n")
}

func (s *target) dockerfileFromPath(path string, list targetlist) string {
	os.Chdir(filepath.Join(origdir, path))
	data, err := ioutil.ReadFile("Dockerfile")
	if err != nil {
		log.Fatalf("Failed to read image: %s: %v", s.image, err)
		return ""
	}
	return strings.Trim(string(data), " \r\n")
}

func main() {
	runTargetNames, err := flags.Parse(&opts)
	if err != nil {
		os.Exit(1)
	}

	if opts.Version {
		fmt.Println("drmake " + version)
		return
	}

	origdir, _ = os.Getwd()
	tempdir, _ = ioutil.TempDir("", "")
	defer os.RemoveAll(tempdir)

	list := targetlist{}
	defaultTarget := parseMakefile(list)
	if len(runTargetNames) == 0 {
		runTargetNames = []string{defaultTarget}
	}

	if opts.PrintList {
		print(list)
		return
	}

	run(list, runTargetNames)
}

func print(list targetlist) {
	longest := 0
	namelist := []string{}
	for name, target := range list {
		if l := len(name); l > longest {
			if target.desc == "" {
				continue
			}
			longest = l
			namelist = append(namelist, name)
		}
	}
	slongest := strconv.Itoa(longest)

	sort.Strings(namelist)
	for _, name := range namelist {
		target := list[name]
		fmt.Printf("drmake %-"+slongest+"s # %s\n", name, target.desc)
	}
}

func run(list targetlist, runTargetNames []string) {
	if len(runTargetNames) == 0 {
		runTargetNames = []string{defaultTarget}
	}
	runTargets := buildExecOrder(list, runTargetNames)
	orderedTargets := make([]string, len(runTargets))
	for i, s := range runTargets {
		orderedTargets[i] = s.name
	}
	prepVolume()
	for _, target := range runTargets {
		target.Run(list)
	}
}

func parseMakefile(list targetlist) (defaultTarget string) {
	var atarget *target
	data, err := ioutil.ReadFile(opts.Makefile)
	if err != nil {
		log.Fatalf("Failed to find %s: %v", opts.Makefile, err)
		return
	}

	lines := strings.Split(string(data), "\n")
	prev := ""
	for _, line := range lines {
		line = prev + strings.Trim(line, " \r\n")
		if strings.HasSuffix(line, " \\") {
			prev = line[0 : len(line)-1]
			continue
		} else {
			prev = ""
		}
		if line == "" || line[0] == '#' {
			continue
		}

		c := strings.Fields(line)
		if len(c) > 0 && strings.ToUpper(c[0]) == "FROM" {
			match := reFromLine.FindStringSubmatch(line)
			if len(match) < 2 {
				continue
			}

			image := match[1]
			name := match[2]
			deps := strings.Fields(match[3])
			if name == "" {
				c := regexp.MustCompile(`\b`).Split(image, -1)
				name = c[len(c)-1]
			}

			atarget = &target{
				name:      name,
				image:     image,
				deps:      deps,
				artifacts: map[string]string{},
			}
			list[atarget.name] = atarget
			if defaultTarget == "" {
				defaultTarget = atarget.name
			}
			continue
		}

		if atarget == nil {
			continue
		}

		if len(c) > 1 && strings.ToUpper(c[0]) == "ARTIFACT" {
			var src string
			var dst string
			artargs := strings.Join(c[1:], " ")
			splitchr := " "
			if strings.Contains(artargs, "=") {
				splitchr = "="
			}

			s := strings.SplitN(artargs, splitchr, 2)
			src = s[0]
			if len(s) == 2 {
				dst = s[1]
			} else {
				dst = s[0]
			}
			atarget.artifacts[src] = dst
			continue
		}

		if len(c) > 1 && strings.ToUpper(c[0]) == "ENVARG" {
			atarget.defn += line[3:] + "\n"
			if len(c) != 2 {
				log.Fatal("ENVARG requires exactly one argument")
			}
			parts := strings.SplitN(c[1], "=", 2)
			atarget.defn += fmt.Sprintf("ENV %s=${%s}\n", parts[0], parts[0])
			continue
		}

		if len(c) > 1 && strings.ToUpper(c[0]) == "LABEL" {
			kv := strings.SplitN(strings.Join(c[1:], " "), "=", 2)
			if len(kv) == 2 && strings.ToLower(strings.Trim(kv[0], `"`)) == "description" {
				atarget.desc = strings.Trim(kv[1], `"`)
			}
		}

		atarget.defn += line + "\n"
	}
	return
}

func buildExecOrder(list targetlist, targets []string) (out []*target) {
	unordTargets := []string{}
	ordTargets := map[string]int{}

	for _, targName := range targets {
		target := list.find(targName)
		depTargets := buildExecOrder(list, target.deps)
		depTargetNames := make([]string, len(depTargets))
		for i, s := range depTargets {
			depTargetNames[i] = s.name
		}
		unordTargets = append(unordTargets, append(append([]string{}, depTargetNames...), targName)...)
	}

	n := 0
	for _, name := range unordTargets {
		if ordTargets[name] != 0 {
			continue
		}

		n++
		ordTargets[name] = n
	}

	out = make([]*target, len(ordTargets))
	for name, idx := range ordTargets {
		out[idx-1] = list.find(name)
	}

	return
}

func prepVolume() {
	if opts.Host {
		return
	}

	vols := []string{wsvol(), cachevol()}

	for _, vol := range vols {
		if opts.Fresh {
			cmd := exec.Command("docker", "volume", "rm", "-f", vol)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Run()
		}
	}

	cmd := exec.Command("docker", "volume", "create", wsvol())
	if err := cmd.Run(); err == nil {
		copyVol("/srv/.", "/work")
	}
}

func copyVolAll(src, dst string) error {
	if opts.Host {
		return nil
	}
	finaldst := dst
	if !strings.HasSuffix(finaldst, "/") {
		finaldst = path.Dir(finaldst)
	}
	var dir string
	if finaldst == "/srv" {
		dir = origdir
	} else if strings.HasPrefix(finaldst, "/srv/") {
		dir = filepath.FromSlash(strings.Replace(finaldst, "/srv/", origdir+"/", 1))
	}
	os.MkdirAll(dir, 0775)
	return copyVol(src, dst)
}

func copyVol(src, dst string) error {
	if opts.Host {
		return nil
	}
	log.Printf("Copying data: %s -> %s\n", src, dst)
	cmd := exec.Command("docker", "run", "--rm", "-v", origdir+":/srv", "-v",
		wsvol()+":/work", "alpine", "sh", "-c", "cp -R "+src+" "+dst)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func wsvol() string {
	if opts.Host {
		return origdir
	}
	return fmt.Sprintf("drmake-ws-%x", sha1.Sum([]byte(opts.Makefile)))
}

func cachevol() string {
	return fmt.Sprintf("drmake-cache-%x", sha1.Sum([]byte(opts.Makefile)))
}

func image() string {
	return fmt.Sprintf("drmake-%x", sha1.Sum([]byte(opts.Makefile)))
}
