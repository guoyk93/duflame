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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/guoyk93/rg"
)

var (
	//go:embed template.gohtml
	res embed.FS
)

func serializeAttributes(m map[string]string) template.HTMLAttr {
	var items []string
	for k, v := range m {
		items = append(items, fmt.Sprintf("%s=%s", k, strconv.Quote(v)))
	}
	return template.HTMLAttr(strings.Join(items, " "))
}

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

func CompactUsage(usage *Usage, maxEntries int, maxDepth int) {
	sort.SliceStable(usage.Entries, func(i, j int) bool {
		return usage.Entries[i].Size > usage.Entries[j].Size
	})

	if len(usage.Entries) > maxEntries {
		newEntries := make([]*Usage, maxEntries+1)
		copy(newEntries, usage.Entries[:maxEntries])

		var othersSize int64
		for _, entry := range usage.Entries[maxEntries:] {
			othersSize += entry.Size
		}

		newEntries[maxEntries] = &Usage{
			Parent: usage,
			Name:   "[Others]",
			Size:   othersSize,
		}

		usage.Entries = newEntries
	}

	{
		var depth int
		usage := usage
		for usage.Parent != nil {
			depth += 1
			if depth > maxDepth {
				return
			}
			usage = usage.Parent
		}
	}

	for _, entry := range usage.Entries {
		CompactUsage(entry, maxEntries, maxDepth)
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
		optPath       string
		optOutput     string
		optMaxEntries int
		optMaxDepth   int
	)
	flag.StringVar(&optPath, "C", ".", "directory path")
	flag.StringVar(&optOutput, "o", "duflame.html", "output file path")
	flag.IntVar(&optMaxEntries, "t", 10, "max entries for each directory")
	flag.IntVar(&optMaxDepth, "d", 10, "max depth")
	flag.Parse()

	if optMaxEntries < 1 {
		optMaxEntries = 1
	}

	if optMaxDepth < 1 {
		optMaxDepth = 1
	}

	tpl := rg.Must(
		template.New("__main__").Funcs(template.FuncMap{
			"calculateItemAttributes": func(usage *Usage) template.HTMLAttr {
				attrClass := "flamegraph-item"
				attrStyle := "width: 100%;"
				if usage.Parent != nil && usage.Parent.Size != 0 {
					ratio := float64(usage.Size) / float64(usage.Parent.Size)
					attrStyle = fmt.Sprintf(
						"width: %.2f%%;",
						ratio*100,
					)
				}
				return serializeAttributes(map[string]string{
					"class": attrClass,
					"style": attrStyle,
				})
			},
			"calculateItemTitleAttributes": func(usage *Usage) template.HTMLAttr {
				var names []string

				if usage.Parent == nil {
					names = []string{usage.Name}
				} else {
					usage := usage
					for usage.Parent != nil {
						names = append([]string{usage.Name}, names...)
						usage = usage.Parent
					}
				}

				attrDataPath := filepath.Join(names...)
				attrDataSize := strconv.FormatInt(usage.Size, 10)
				attrClass := "flamegraph-item-title"

				if usage.Parent == nil {
					attrClass += " flamegraph-item-title-top"
				} else if len(usage.Parent.Entries) > 0 &&
					usage.Parent.Entries[len(usage.Parent.Entries)-1] == usage {
					attrClass += " flamegraph-item-title-end"
				}

				attrStyle := "background-color: azure;"

				if usage.Parent != nil && usage.Parent.Size != 0 {
					ratio := float64(usage.Size) / float64(usage.Parent.Size)
					attrStyle = fmt.Sprintf(
						`background-color: rgb(%d, 255, 255);`,
						int(100+(1.0-ratio)*156.0),
					)
				}

				return serializeAttributes(map[string]string{
					"style":       attrStyle,
					"data-path":   attrDataPath,
					"data-size":   attrDataSize,
					"class":       attrClass,
					"onmouseover": "onItemHover(this)",
				})
			},
		}).Parse(string(
			rg.Must(res.ReadFile("template.gohtml")),
		)),
	)

	f := rg.Must(os.OpenFile(optOutput, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644))
	defer f.Close()

	usage := &Usage{
		Name: "[ROOT]",
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

	CompactUsage(usage, optMaxEntries, optMaxDepth)

	err = tpl.Execute(f, map[string]any{
		"Time":     time.Now().Format(time.DateTime),
		"Hostname": rg.Must(os.Hostname()),
		"Path":     rg.Must(filepath.Abs(optPath)),
		"Usage":    usage,
	})
}
