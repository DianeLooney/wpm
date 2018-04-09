package main

import (
	"archive/zip"
	"bytes"
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

type Manifest struct {
	Installations []Installation
}

type Installation struct {
	Dir    string
	Addons []*Spec
}

type Spec struct {
	Name     string
	Type     string
	Location string `yaml:",omitempty"`
	Branch   string `yaml:",omitempty"`

	zipData *zip.Reader `yaml:"-"`
	ownDirs []string    `yaml:"-"`
}

var errFileNotFound = fmt.Errorf("wpm: file not found")
var errFileFormat = fmt.Errorf("wpm: file not formatted correctly")

func readManifest() (*Manifest, error) {
	ad := os.Getenv("APPDATA")
	wpm := path.Join(ad, "wpm.yaml")
	d, err := ioutil.ReadFile(wpm)
	if err != nil {
		return nil, errFileNotFound
	}
	m := Manifest{}
	err = yaml.Unmarshal(d, &m)
	if err != nil {
		return nil, errFileFormat
	}
	return &m, nil
}
func main() {
	args := os.Args[1:]
	switch args[0] {
	case "init":
		i := Installation{}
		d, err := yaml.Marshal(i)
		if err != nil {
			log.Fatalf("Internal error: unable to create yaml: %v\n", err)
		}
		ad := os.Getenv("APPDATA")
		wpm := path.Join(ad, "wpm.yaml")
		err = ioutil.WriteFile(wpm, d, 0644)
		if err != nil {
			log.Fatalf("Unable to write to file: %v\n", err)
		}
	case "list":
		m, err := readManifest()
		if err != nil {
			log.Fatalf("Unable to load wpm.yaml: %v\n", err)
		}
		d, _ := yaml.Marshal(m)
		fmt.Printf("%s\n", d)
	case "add":
		log.Fatalf("Not yet implemented")
	case "upgrade":
		m, err := readManifest()
		if err != nil {
			log.Fatalf("Unable to load wpm.yaml: %v\n", err)
		}
		//mtx := sync.Mutex{}
		wg := sync.WaitGroup{}
		wg.Add(len(m.Installations[0].Addons))
		for i, adn := range m.Installations[0].Addons {
			go func(i int, adn *Spec) {
				defer wg.Done()
				adn.Download()
			}(i, adn)
		}
		wg.Wait()
		//check for conflicts

		wg = sync.WaitGroup{}
		for _, adn := range m.Installations[0].Addons {
			wg.Add(1)
			go func(adn *Spec) {
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

type change interface {
	commit() error
}

type removeAll struct {
	loc string
}

func (a removeAll) commit() error {
	return os.RemoveAll(a.loc)
}

type remove struct {
	loc string
}

func (a remove) commit() error {
	return os.Remove(a.loc)
}

type addDir struct {
	loc string
}

func (a addDir) commit() error {
	return os.Mkdir(a.loc, 0666)
}

type addFile struct {
	loc  string
	data io.Reader
}

func (a addFile) commit() error {
	data, _ := ioutil.ReadAll(a.data)
	return ioutil.WriteFile(a.loc, data, 0644)
}

type pack struct {
	ownDirs []string
	data    *zip.Reader
}

func (sp *Spec) Download() {
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
		//do nothing to it
	}
}
func (sp *Spec) PlanChanges(base string) []change {
	switch sp.Type {
	case "curse":
		fallthrough
	case "wowace":
		dirs := make(map[string]bool)
		if sp.zipData == nil {
			fmt.Println("nil data")
			return make([]change, 0)
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
		retval := make([]change, 0)
		for _, d := range sp.ownDirs {
			retval = append(retval, removeAll{path.Join(base, d)})
		}
		for _, s := range dirSl {
			retval = append(retval, addDir{path.Join(base, s)})
		}
		for _, f := range sp.zipData.File {
			if f.FileInfo().IsDir() {
				continue
			}
			rd, _ := f.Open()
			retval = append(retval, addFile{path.Join(base, f.Name), rd})
		}
		return retval
	case "ignore":
		return make([]change, 0)
	}
	return make([]change, 0)
}
