# 网站Sitemap生成

该程序是一个简单的网站Sitemap生成器，用于生成一个网站Sitemap，并上传到服务器。

采用的技术是 Go + Sciter。

该程序仅做测试，不保证可用性。

## 如何运行

拉取本项目到本地，在项目的目录下运行命令：

```bash
git clone https://github.com/fesiong/sitemap.git
cd sitemap
go mod tidy
go run anqicms.com/sitemap
```

## 用到了的库

- [github.com/PuerkitoBio/goquery](https://github.com/PuerkitoBio/goquery)  
- [github.com/chromedp/cdproto](https://github.com/chromedp/cdproto)  
- [github.com/chromedp/chromedp](https://github.com/chromedp/chromedp)  
- [github.com/h2non/filetype](https://github.com/h2non/filetype)  
- [github.com/labstack/gommon](https://github.com/labstack/gommon)  
- [github.com/sciter-sdk/go-sciter](https://github.com/sciter-sdk/go-sciter)  
- [github.com/skratchdot/open-golang](https://github.com/skratchdot/open-golang)  