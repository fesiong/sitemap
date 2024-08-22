package main

import (
	"anqicms.com/sitemap/utils"
	"context"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"log"
	"net"
	"time"
)

var chromeCookies []*network.CookieParam

func ChromeDPGetArticle(link string) (string, error) {
	var res interface{}
	ops := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.NoDefaultBrowserCheck,
		//chromedp.Flag("headless", false),
		chromedp.Flag("ignore-certificate-errors", true),
		chromedp.Flag("disable-web-security", true),
		chromedp.NoFirstRun,
		chromedp.UserAgent(utils.DefaultUserAgent),
	)
	allocCtx, _ := GetChromeContext(ops, false)

	ctx, cancel := chromedp.NewContext(
		allocCtx,
		chromedp.WithLogf(log.Printf),
	)
	defer cancel()

	ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var content string
	var err error
	err = chromedp.Run(ctx,
		chromedp.ActionFunc(func(c context.Context) error {
			_ = network.SetCookies(chromeCookies).Do(c)
			return nil
		}),
		chromedp.Navigate(link),
		chromedp.Evaluate(`delete navigator.__proto__.webdriver;`, &res),
		chromedp.Sleep(2*time.Second),
		chromedp.OuterHTML("html", &content),
	)
	if err != nil {
		return "", err
	}

	return content, nil
}

var isRemoteChrome = false

// ChromeCtx 使用一个实例
var allocOpts []chromedp.ExecAllocatorOption

func getAllocOpts() []chromedp.ExecAllocatorOption {

	if allocOpts == nil {
		allocOpts = chromedp.DefaultExecAllocatorOptions[:]
		allocOpts = append(allocOpts,
			// 暂时开启
			//chromedp.Flag("headless", false),
			chromedp.Flag("disable-software-rasterizer", true),
			chromedp.Flag("blink-settings", "imagesEnabled=false"),
			chromedp.UserAgent(utils.DefaultUserAgent),
			chromedp.Flag("accept-language", `zh-CN,zh;q=0.9,en-US;q=0.8,en;q=0.7,zh-TW;q=0.6`),
		)
	}

	return allocOpts
}

func GetChromeContext(ops []chromedp.ExecAllocatorOption, forceRemote bool) (ctx context.Context, cancel context.CancelFunc) {
	log.Println("is remote chrome", isRemoteChrome)
	if isRemoteChrome && forceRemote {
		// 不知道为何，不能直接使用 NewExecAllocator ，因此增加 使用 ws://127.0.0.1:9222/ 来调用
		ctx, cancel = chromedp.NewRemoteAllocator(context.Background(), "ws://127.0.0.1:9222/")
	} else {
		ctx, cancel = chromedp.NewExecAllocator(context.Background(), ops...)
	}
	return
}

func init() {
	checkRemoveChrome()
}

func checkRemoveChrome() {
	// 不知道为何，不能直接使用 NewExecAllocator ，因此增加 使用 ws://127.0.0.1:9222/ 来调用
	addr := net.JoinHostPort("", "9222")
	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		isRemoteChrome = false
	} else {
		isRemoteChrome = true
		_ = conn.Close()
	}
}
