package main

import (
	"anqicms.com/sitemap/utils"
	"embed"
	"encoding/json"
	"github.com/sciter-sdk/go-sciter"
	"github.com/sciter-sdk/go-sciter/window"
	"github.com/skratchdot/open-golang/open"
	"log"
	"os"
	"path/filepath"
	"strings"
)

//go:embed all:views
var views embed.FS

type Map map[string]interface{}

func main() {
	w, err := window.New(sciter.SW_TITLEBAR|sciter.SW_RESIZEABLE|sciter.SW_CONTROLS|sciter.SW_MAIN|sciter.SW_ENABLE_DEBUG, &sciter.Rect{
		Left:   100,
		Top:    50,
		Right:  1100,
		Bottom: 660,
	})
	if err != nil {
		log.Fatal(err)
	}

	w.SetCallback(&sciter.CallbackHandler{
		OnLoadData: func(params *sciter.ScnLoadData) int {
			if strings.HasPrefix(params.Uri(), "home://") {
				fileData, err := views.ReadFile(params.Uri()[7:])
				if err == nil {
					w.DataReady(params.Uri()[7:], fileData)
				}
			}
			return 0
		},
	})

	w.DefineFunction("openUrl", openUrl)
	w.DefineFunction("getRunningTask", getRunningTask)
	w.DefineFunction("createTask", createTask)

	mainView, err := views.ReadFile("views/main.html")
	if err != nil {
		os.Exit(0)
	}
	w.LoadHtml(string(mainView), "")

	w.SetTitle("Sitemap 生成")
	w.Show()
	w.Run()
}

func openUrl(args ...*sciter.Value) *sciter.Value {
	link := args[0].String()
	_ = open.Run(link)

	return nil
}

// 获取运行中的task
func getRunningTask(args ...*sciter.Value) *sciter.Value {
	if RunningCrawler == nil {
		return nil
	}
	return jsonValue(RunningCrawler)
}

// 创建任务
func createTask(args ...*sciter.Value) *sciter.Value {
	domain := args[0].String()
	exePath, _ := os.Executable()
	sitemapPath := filepath.Dir(exePath) + "/" + utils.GetMd5String(domain, false, true) + ".txt"
	crawler, err := NewCrawler(CrawlerTypeSitemap, domain, sitemapPath)
	if err != nil {
		return jsonValue(Map{
			"msg":    err.Error(),
			"status": -1,
		})
	}
	crawler.OnFinished = func() {
		// 完成时处理函数
	}
	crawler.Start()

	return jsonValue(Map{
		"msg":    "任务已创建",
		"status": 1,
	})
}

func jsonValue(val interface{}) *sciter.Value {
	buf, err := json.Marshal(val)
	if err != nil {
		return nil
	}
	return sciter.NewValue(string(buf))
}
