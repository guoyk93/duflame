package main

import (
	"embed"
	"flag"
	"fmt"
	"html/template"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
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
	atomic.AddInt64(&u.Size, size)
	if u.Parent != nil {
		u.Parent.AddSize(size)
	}
}

type CreateUsageOptions struct {
	Concurrency chan struct{}
	Dir         string
	OnError     func(err error, dir string)
	WaitGroup   *sync.WaitGroup
}

func CreateUsage(usage *Usage, opts CreateUsageOptions) {
	// concurrency control
	<-opts.Concurrency
	defer func() {
		opts.Concurrency <- struct{}{}
	}()
	defer opts.WaitGroup.Done()

	// error handling
	var err error
	defer func() {
		if err == nil {
			return
		}
		if opts.OnError != nil {
			opts.OnError(err, opts.Dir)
		}
	}()
	defer rg.Guard(&err)

	entries := rg.Must(os.ReadDir(opts.Dir))

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		info := rg.Must(entry.Info())

		usage.AddSize(info.Size())
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		subUsage := &Usage{
			Parent: usage,
			Name:   entry.Name(),
		}
		usage.Entries = append(usage.Entries, subUsage)

		opts.WaitGroup.Add(1)

		go CreateUsage(subUsage, CreateUsageOptions{
			Concurrency: opts.Concurrency,
			WaitGroup:   opts.WaitGroup,
			Dir:         filepath.Join(opts.Dir, entry.Name()),
			OnError:     opts.OnError,
		})
	}
	return
}

func CompactUsage(usage *Usage, top int) {
	sort.SliceStable(usage.Entries, func(i, j int) bool {
		return usage.Entries[i].Size > usage.Entries[j].Size
	})

	if len(usage.Entries) > top {
		newEntries := make([]*Usage, top+1)
		copy(newEntries, usage.Entries[:top])

		var othersSize int64
		for _, entry := range usage.Entries[top:] {
			othersSize += entry.Size
		}

		newEntries[top] = &Usage{
			Name: "[Others]",
			Size: othersSize,
		}

		usage.Entries = newEntries
	}

	for _, entry := range usage.Entries {
		CompactUsage(entry, top)
	}
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
		optTop    int
	)
	flag.StringVar(&optPath, "C", ".", "directory path")
	flag.StringVar(&optOutput, "o", "duflame.html", "output file path")
	flag.IntVar(&optTop, "t", 10, "top entries for each directory")
	flag.Parse()

	if optTop < 1 {
		optTop = 1
	}

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

	// concurrency
	numCPU := runtime.NumCPU()
	concurrency := make(chan struct{}, numCPU)
	for i := 0; i < numCPU; i++ {
		concurrency <- struct{}{}
	}

	// wait group
	waitGroup := &sync.WaitGroup{}
	waitGroup.Add(1)

	CreateUsage(usage, CreateUsageOptions{
		Concurrency: concurrency,
		WaitGroup:   waitGroup,
		Dir:         optPath,
		OnError: func(err error, dir string) {
			log.Println("failed to calculate usage:", err, dir)
		},
	})

	waitGroup.Wait()

	CompactUsage(usage, optTop)

	err = tpl.Execute(f, map[string]any{
		"Time":     time.Now().Format(time.DateTime),
		"Hostname": rg.Must(os.Hostname()),
		"Path":     rg.Must(filepath.Abs(optPath)),
		"Usage":    usage,
	})
}
