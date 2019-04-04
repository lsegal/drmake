package main

import (
	"crypto/sha1"
	"flag"
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
)

const (
	defaultStage = "default"
)

var (
	makefile  = flag.String("f", "Makefile.phd", "The build file to parse stages from")
	fresh     = flag.Bool("fresh", false, "If fresh image should be used")
	host      = flag.Bool("host", false, "Mount to host volume")
	printList = flag.Bool("s", false, "List stages")

	tempdir string
	origdir string

	reFromLine = regexp.MustCompile(`(?i)^FROM\s+(\S+)(?:\s+AS\s+(\S+))?(?:\s+USING\s+(.+)$)?`)
)

type stage struct {
	name  string
	image string
	defn  string
	desc  string
	deps  []string

	artifacts map[string]string
}

type stagelist map[string]*stage

func (s stagelist) find(name string) *stage {
	if s[name] == nil {
		log.Fatal("Unknown target: ", name)
		return nil
	}
	return s[name]
}

func (s *stage) String() string {
	return fmt.Sprintf("stage %s FROM %s: %s\n%s",
		s.name, s.image, strings.Join(s.deps, " "), s.defn)
}

func (s *stage) Run(list stagelist) {
	dfile := s.Dockerfile(list)
	if dfile != "" || !strings.HasPrefix(s.image, "#") {
		cmd := exec.Command("docker", "build", "--rm", "-t", image()+"/"+s.name, "-")
		cmd.Stdin = strings.NewReader(dfile)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Run()

		cmd = exec.Command("docker", "run", "--rm", "-v",
			vol()+":/work", "-w", "/work", "-it", image()+"/"+s.name)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Run()
	}

	if len(s.artifacts) > 0 {
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

func (s *stage) Dockerfile(list stagelist) string {
	preface := "FROM " + s.image
	if strings.HasPrefix(s.image, "&") {
		preface = s.dockerfileFromPath(filepath.Join(".drmake", "stages", s.image[1:]), list)
	} else if strings.HasPrefix(s.image, "#") {
		if s.image[1:] == s.name {
			return ""
		}
		prestage := list.find(s.image[1:])
		preface = strings.Trim(prestage.Dockerfile(list), " \r\n")
	} else if strings.HasPrefix(s.image, "./") {
		preface = s.dockerfileFromPath(s.image[2:], list)
	}
	os.Chdir(tempdir)
	return strings.Join([]string{preface, s.defn}, "\n")
}

func (s *stage) dockerfileFromPath(path string, list stagelist) string {
	os.Chdir(filepath.Join(origdir, path))
	data, err := ioutil.ReadFile("Dockerfile")
	if err != nil {
		log.Fatalf("Failed to read image: %s: %v", s.image, err)
		return ""
	}
	return strings.Trim(string(data), " \r\n")
}

func main() {
	flag.Parse()

	origdir, _ = os.Getwd()
	tempdir, _ = ioutil.TempDir("", "")
	defer os.RemoveAll(tempdir)

	list := stagelist{}
	defaultStage := parseMakefile(list)

	if *printList {
		print(list)
		return
	}

	run(list, defaultStage)
}

func print(list stagelist) {
	longest := 0
	namelist := []string{}
	for name, stage := range list {
		if l := len(name); l > longest {
			if stage.desc == "" {
				continue
			}
			longest = l
			namelist = append(namelist, name)
		}
	}
	slongest := strconv.Itoa(longest)

	sort.Strings(namelist)
	for _, name := range namelist {
		stage := list[name]
		fmt.Printf("drmake %-"+slongest+"s # %s\n", name, stage.desc)
	}
}

func run(list stagelist, defaultStage string) {
	runStageNames := flag.Args()
	if len(runStageNames) == 0 {
		runStageNames = []string{defaultStage}
	}
	runStages := buildExecOrder(list, runStageNames)
	orderedStages := make([]string, len(runStages))
	for i, s := range runStages {
		orderedStages[i] = s.name
	}
	prepVolume()
	for _, stage := range runStages {
		stage.Run(list)
	}
}

func parseMakefile(list stagelist) (defaultStage string) {
	var astage *stage
	data, err := ioutil.ReadFile(*makefile)
	if err != nil {
		log.Fatalf("Failed to find %s: %v", *makefile, err)
		return
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.Trim(line, " \r\n")
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

			astage = &stage{
				name:      name,
				image:     image,
				deps:      deps,
				artifacts: map[string]string{},
			}
			list[astage.name] = astage
			if defaultStage == "" {
				defaultStage = astage.name
			}
			continue
		}

		if astage == nil {
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
			astage.artifacts[src] = dst
			continue
		}

		if len(c) > 1 && strings.ToUpper(c[0]) == "LABEL" {
			kv := strings.SplitN(strings.Join(c[1:], " "), "=", 2)
			if len(kv) == 2 && strings.ToLower(strings.Trim(kv[0], `"`)) == "description" {
				astage.desc = strings.Trim(kv[1], `"`)
			}
		}

		astage.defn += line + "\n"
	}
	return
}

func buildExecOrder(list stagelist, targets []string) (out []*stage) {
	unordStages := []string{}
	ordStages := map[string]int{}

	for _, target := range targets {
		stage := list.find(target)
		depStages := buildExecOrder(list, stage.deps)
		depStageNames := make([]string, len(depStages))
		for i, s := range depStages {
			depStageNames[i] = s.name
		}
		unordStages = append(unordStages, append(append([]string{}, depStageNames...), target)...)
	}

	n := 0
	for _, name := range unordStages {
		if ordStages[name] != 0 {
			continue
		}

		n++
		ordStages[name] = n
	}

	out = make([]*stage, len(ordStages))
	for name, idx := range ordStages {
		out[idx-1] = list.find(name)
	}

	return
}

func prepVolume() {
	if *host {
		return
	}

	volname := vol()
	if *fresh {
		cmd := exec.Command("docker", "volume", "rm", "-f", volname)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Run()
	}

	cmd := exec.Command("docker", "volume", "create", volname)
	if err := cmd.Run(); err == nil {
		copyVol("/srv/.", "/work")
	}
}

func copyVolAll(src, dst string) error {
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
	cmd := exec.Command("docker", "run", "--rm", "-v", origdir+":/srv", "-v",
		vol()+":/work", "alpine", "sh", "-c", "cp -R "+src+" "+dst)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func vol() string {
	if *host {
		return origdir
	}
	return image()
}

func image() string {
	return fmt.Sprintf("drmake-%x", sha1.Sum([]byte(*makefile)))
}
