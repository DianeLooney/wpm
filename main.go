package main

import (
	"archive/zip"
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"sort"
	"sync"

	"github.com/PuerkitoBio/goquery"
	yaml "gopkg.in/yaml.v2"
)

type Config struct {
	Installations []Installation
}

type Installation struct {
	Dir    string
	Addons []*Specification
}

type Specification struct {
	Name     string
	Type     string
	Location string `yaml:",omitempty"`
	Branch   string `yaml:",omitempty"`

	zipData *zip.Reader `yaml:"-"`
	ownDirs []string    `yaml:"-"`
}

var errFileNotFound = fmt.Errorf("wpm: file not found")
var errFileFormat = fmt.Errorf("wpm: file not formatted correctly")
var defaultInstallLocation = `C:\Program Files (x86)\World of Warcraft\Interface\AddOns`

func wpmLocation() string {
	return path.Join(os.Getenv("APPDATA"), "wpm", "wpm.yaml")
}

func readConfig() (*Config, error) {
	loc := wpmLocation()
	d, err := ioutil.ReadFile(loc)
	if err != nil {
		return nil, errFileNotFound
	}
	cfg := Config{}
	err = yaml.Unmarshal(d, &cfg)
	if err != nil {
		return nil, errFileFormat
	}
	return &cfg, nil
}
func saveConfig(c *Config) error {
	d, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("Internal error: unable to create yaml: %v\n", err)
	}
	wpm := wpmLocation()
	return ioutil.WriteFile(wpm, d, 0644)
}

func main() {
	if len(os.Args) < 2 {
		/*
			fmt.Println("wpm commands:")
			fmt.Println("\twpm init")
			fmt.Println("\t\tcreates a wpm.yaml config file located in APPDATA")
			fmt.Println("\twpm list [-path=\"path\"]")
			fmt.Println("\t\tlists all of the installed addons")
			fmt.Println("\t\tif supplied with a path, will list only that installations addons")
			fmt.Println("\twpm add [-i=path] -n=name -t=curse|wowace|ignore")
			fmt.Println("\t\tadds the addon to wpm.yaml config")
			fmt.Println("\twpm upgrade [-purge] [-clean]")
		*/
	}
	args := os.Args[1:]
	switch args[0] {
	case "init":
		cfg := Config{}
		cfg.Installations = make([]Installation, 1)
		cfg.Installations[0].Dir = defaultInstallLocation
		cfg.Installations[0].Addons = make([]*Specification, 0)
		err := saveConfig(&cfg)
		if err != nil {
			log.Fatalf("Unable to write to file: %v\n", err)
		}
	case "list":
		cfg, err := readConfig()
		if err != nil {
			log.Fatalf("Unable to load wpm.yaml: %v\n", err)
		}
		fset := flag.NewFlagSet("list args", flag.ContinueOnError)
		f := fset.String("p", defaultInstallLocation, "will list only that installations addons")
		fset.Parse(args[1:])
		if f == nil || *f == "" {
			d, _ := yaml.Marshal(cfg)
			fmt.Printf("%s\n", d)
			return
		}
		for _, i := range cfg.Installations {
			if i.Dir != *f {
				continue
			}
			d, _ := yaml.Marshal(i)
			fmt.Printf("%s\n", d)
			return
		}
		fmt.Printf("No installations handled at '%v'\n", *f)
	case "add":
		cfg, err := readConfig()
		if err != nil {
			log.Fatalf("Unable to load wpm.yaml: %v\n", err)
		}

		fset := flag.NewFlagSet("list args", flag.ContinueOnError)
		pth := fset.String("i", cfg.Installations[0].Dir, "chooses installation location")
		n := fset.String("n", "", "name")
		t := fset.String("t", "", "type")
		l := fset.String("l", "", "location")
		fset.Parse(args[1:])

		for i, v := range cfg.Installations {
			if v.Dir != *pth {
				continue
			}
			cfg.Installations[i].Addons = append(v.Addons, &Specification{
				Name:     *n,
				Type:     *t,
				Location: *l,
			})
		}

		saveConfig(cfg)
		if err != nil {
			log.Fatalf("Unable to write to file: %v\n", err)
		}
	case "upgrade":
		m, err := readConfig()
		if err != nil {
			log.Fatalf("Unable to load wpm.yaml: %v\n", err)
		}

		wg := sync.WaitGroup{}
		wg.Add(len(m.Installations[0].Addons))
		for _, adn := range m.Installations[0].Addons {
			go func(adn *Specification) {
				defer wg.Done()
				adn.Download()
			}(adn)
		}
		wg.Wait()

		//todo: check for conflicts

		wg = sync.WaitGroup{}
		for _, adn := range m.Installations[0].Addons {
			wg.Add(1)
			go func(adn *Specification) {
				defer wg.Done()
				delta := adn.PlanChanges(m.Installations[0].Dir)
				for _, d := range delta {
					d.commit()
				}
			}(adn)
		}
		wg.Wait()
	}
}

type pack struct {
	ownDirs []string
	data    *zip.Reader
}

func (sp *Specification) Download() {
	switch sp.Type {
	case "curse":
		fallthrough
	case "wowace":
		var u string
		switch sp.Type {
		case "curse":
			u = fmt.Sprintf("https://wow.curseforge.com/projects/%v/files", sp.Name)
		case "wowace":
			u = fmt.Sprintf("https://www.wowace.com/projects/%v/files", sp.Name)
		}
		if u == "" {
			return
		}

		resp, err := http.Get(u)
		if err != nil {
			fmt.Printf("Unable to get the index for %v: %v\n", sp.Name, err)
			return
		}
		defer resp.Body.Close()
		doc, err := goquery.NewDocumentFromReader(resp.Body)
		if err != nil {
			fmt.Printf("Unable to parse the returned document into goquery: %v", err)
			return
		}
		items := doc.Find("table.project-file-listing tr.project-file-list-item")
		items.First().Each(func(i int, s *goquery.Selection) {
			//phase, _ := s.Find("td.project-file-release-type>div").Attr("class")
			href, _ := s.Find("div.project-file-download-button a.button.tip.fa-icon-download").Attr("href")
			switch sp.Type {
			case "curse":
				href = "https://wow.curseforge.com" + href
			case "wowace":
				href = "https://www.wowace.com" + href
			}
			r, err := http.Get(href)
			if err != nil {
				fmt.Printf("Error getting zip.")
				return
			}
			b, _ := ioutil.ReadAll(r.Body)
			rd := bytes.NewReader(b)
			sp.zipData, _ = zip.NewReader(rd, r.ContentLength)
		})
		dirs := make(map[string]struct{})
		yes := struct{}{}
		for _, f := range sp.zipData.File {
			if f.Name == "." {
				continue
			}
			dir, _ := path.Split(f.Name)
			for {
				nxt := path.Join(dir, "..")
				if nxt == "." {
					break
				}
				dir = nxt
			}
			dirs[dir] = yes
		}
		sp.ownDirs = make([]string, 0)
		for i := range dirs {
			sp.ownDirs = append(sp.ownDirs, i)
		}
	case "ignore":
		sp.ownDirs = []string{sp.Name}
	case "link":
		sp.ownDirs = []string{sp.Name}
	}
}

func (sp *Specification) PlanChanges(base string) []commiter {
	switch sp.Type {
	case "curse":
		fallthrough
	case "wowace":
		dirs := make(map[string]bool)
		if sp.zipData == nil {
			fmt.Println("nil data")
			return make([]commiter, 0)
		}
		for _, f := range sp.zipData.File {
			pth := path.Dir(f.Name)
			for pth != "." {
				dirs[pth] = true
				pth = path.Dir(pth)
			}
		}
		dirSl := make([]string, len(dirs))
		i := 0
		for k := range dirs {
			dirSl[i] = k
			i++
		}
		sort.Strings(dirSl)
		retval := make([]commiter, 0)
		for _, d := range sp.ownDirs {
			retval = append(retval, fsRmdir{path.Join(base, d)})
		}
		for _, s := range dirSl {
			retval = append(retval, fsMkdir{path.Join(base, s)})
		}
		for _, f := range sp.zipData.File {
			if f.FileInfo().IsDir() {
				continue
			}
			rd, _ := f.Open()
			retval = append(retval, fsWritefile{path.Join(base, f.Name), rd})
		}
		return retval
	case "ignore":
		return make([]commiter, 0)
	case "link":
		ret := make([]commiter, 2)
		ret[0] = fsRmdir{path.Join(base, sp.Name)}
		ret[1] = fsLink{sp.Location, path.Join(base, sp.Name)}
		return ret
	}
	return make([]commiter, 0)
}

// Commiters

type commiter interface {
	commit() error
}

type fsRmdir struct {
	loc string
}

func (a fsRmdir) commit() error {
	return os.RemoveAll(a.loc)
}

type fsRm struct {
	loc string
}

func (a fsRm) commit() error {
	return os.Remove(a.loc)
}

type fsMkdir struct {
	loc string
}

func (a fsMkdir) commit() error {
	return os.Mkdir(a.loc, 0666)
}

type fsWritefile struct {
	loc  string
	data io.Reader
}

func (f fsWritefile) commit() error {
	data, _ := ioutil.ReadAll(f.data)
	return ioutil.WriteFile(f.loc, data, 0644)
}

type fsLink struct {
	src string
	dst string
}

func (f fsLink) commit() error {
	return os.Link(f.src, f.dst)
}
