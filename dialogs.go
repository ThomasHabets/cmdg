package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path"
	"sort"
	"time"

	gc "github.com/rthornton128/goncurses"
)

var (
	errCancel = fmt.Errorf("user cancelled request")
)

type sortFiles []os.FileInfo

func (a sortFiles) Len() int      { return len(a) }
func (a sortFiles) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a sortFiles) Less(i, j int) bool {
	if a[i].IsDir() && !a[j].IsDir() {
		return true
	}
	if !a[i].IsDir() && a[j].IsDir() {
		return false
	}
	return a[i].Name() < a[j].Name()
}

type dotDirs struct {
	name string
	fi   os.FileInfo
}

func (f *dotDirs) Name() string       { return f.name }
func (f *dotDirs) Size() int64        { return f.fi.Size() }
func (f *dotDirs) Mode() os.FileMode  { return f.fi.Mode() }
func (f *dotDirs) ModTime() time.Time { return f.fi.ModTime() }
func (f *dotDirs) IsDir() bool        { return f.fi.IsDir() }
func (f *dotDirs) Sys() interface{}   { return f.fi.Sys() }

// like os.ReadDir, except includes dot and double-dot, and sorted.
func readDir(d string) ([]os.FileInfo, error) {
	dot, err := os.Stat(path.Join(d, "."))
	if err != nil {
		return nil, err
	}

	dots, err := os.Stat(path.Join(d, ".."))
	if err != nil {
		return nil, err
	}

	files, err := ioutil.ReadDir(d)
	if err != nil {
		return nil, err
	}
	sort.Sort(sortFiles(files))
	files = append([]os.FileInfo{
		&dotDirs{name: ".", fi: dot},
		&dotDirs{name: "..", fi: dots},
	}, files...)
	return files, nil
}

// saveFileDialog finds a place to save a file.
// TODO: Allow changing the filename. Tab to the Filename field.
func saveFileDialog(fn string) (string, error) {
	maxY, maxX := winSize()

	w, err := gc.NewWindow(maxY-5, maxX-4, 2, 2)
	if err != nil {
		log.Fatalf("Creating stringChoice window: %v", err)
	}
	defer w.Delete()

	cur := -1
	curDir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	files, err := readDir(curDir)
	if err != nil {
		return "", err
	}

	for {
		w.Clear()
		w.Print(fmt.Sprintf("\n  Filename> %s\n  Current dir: %s\n\n", fn, curDir))
		if cur == -1 {
			w.Print(fmt.Sprintf(" > <save>\n"))
		} else {
			w.Print(fmt.Sprintf("   <save>\n"))
		}
		offset := 0
		if cur > 5 {
			offset = cur - 5
		}
		for n, f := range files {
			if n < offset {
				continue
			}
			printName := f.Name()
			if f.IsDir() {
				printName += "/"
			}
			if n == cur {
				w.Print(fmt.Sprintf(" > %s\n", printName))
			} else {
				w.Print(fmt.Sprintf("   %s\n", printName))
			}
		}
		winBorder(w)
		w.Refresh()
		select {
		case key := <-nc.Input:
			switch key {
			case 'n':
				if cur < len(files)-1 {
					cur++
				}
			case 'p':
				// -1 is OK, it's the OK button.
				if cur >= 0 {
					cur--
				}
			case 'q':
				return "", errCancel
			case '\n', '\r':
				if cur == -1 {
					return path.Join(curDir, fn), nil
				}
				if files[cur].IsDir() {
					newDir := path.Join(curDir, files[cur].Name())
					newFiles, err := readDir(newDir)
					if err == nil {
						curDir = newDir
						files = newFiles
						cur = 0
					}
				}
			}
		}
	}
}
