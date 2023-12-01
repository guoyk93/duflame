package main

import (
	"embed"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/guoyk93/rg"
)

var (
	//go:embed template.gohtml
	res embed.FS
)

type Usage struct {
	Parent  *Usage   `json:"-"`
	Name    string   `json:"name"`
	Size    int64    `json:"size"`
	Entries []*Usage `json:"entries"`
}

func (u *Usage) AddSize(size int64) {
	u.Size += size
	if u.Parent != nil {
		u.Parent.AddSize(size)
	}
}

func CreateUsage(usage *Usage, dir string) (err error) {
	var entries []fs.DirEntry
	if entries, err = os.ReadDir(dir); err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			subUsage := &Usage{
				Parent: usage,
				Name:   entry.Name(),
			}
			if err = CreateUsage(subUsage, filepath.Join(dir, entry.Name())); err != nil {
				return
			}
			usage.Entries = append(usage.Entries, subUsage)
		} else {
			var info fs.FileInfo
			if info, err = entry.Info(); err != nil {
				return
			}
			usage.AddSize(info.Size())
		}
	}
	return
}

func main() {
	var (
		err error
	)

	defer func() {
		if err == nil {
			return
		}
		log.Println("exited with error:", err)
		os.Exit(1)
	}()
	defer rg.Guard(&err)

	var (
		optPath   string
		optOutput string
	)
	flag.StringVar(&optPath, "C", ".", "directory path")
	flag.StringVar(&optOutput, "o", "duflame.html", "output file path")
	flag.Parse()

	tpl := rg.Must(
		template.New("__main__").Funcs(template.FuncMap{
			"calculateDataAttributes": func(usage *Usage) template.HTMLAttr {
				var names []string

				u := usage
				for u.Parent != nil {
					names = append([]string{u.Name}, names...)
					u = u.Parent
				}

				return template.HTMLAttr(
					fmt.Sprintf(
						`data-path=%s data-size="%d"`,
						strconv.Quote(filepath.Join(names...)),
						usage.Size,
					),
				)
			},
			"calculateStyle": func(usage *Usage) template.HTMLAttr {
				if usage.Parent == nil || usage.Parent.Size == 0 {
					return `style="width: 100%;"`
				}
				ratio := float64(usage.Size) / float64(usage.Parent.Size)
				return template.HTMLAttr(
					fmt.Sprintf(
						`style="width: %.2f%%;"`,
						ratio*100,
					),
				)
			},
			"calculateTitleStyle": func(usage *Usage) template.HTMLAttr {
				if usage.Parent == nil || usage.Parent.Size == 0 {
					return `style="background-color: azure;"`
				}
				ratio := float64(usage.Size) / float64(usage.Parent.Size)
				return template.HTMLAttr(
					fmt.Sprintf(
						`style="background-color: rgb(%d, 255, 255);"`,
						int(100+(1.0-ratio)*156.0),
					),
				)
			},
		}).Parse(string(
			rg.Must(res.ReadFile("template.gohtml")),
		)),
	)

	f := rg.Must(os.OpenFile(optOutput, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644))
	defer f.Close()

	usage := &Usage{
		Name: "all",
	}

	rg.Must0(CreateUsage(usage, optPath))

	err = tpl.Execute(f, map[string]any{
		"Time":     time.Now().Format(time.DateTime),
		"Hostname": rg.Must(os.Hostname()),
		"Path":     rg.Must(filepath.Abs(optPath)),
		"Usage":    usage,
	})
}
