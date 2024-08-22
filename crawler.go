package main

import (
	"anqicms.com/sitemap/utils"
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/PuerkitoBio/goquery"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/h2non/filetype"
	"github.com/labstack/gommon/log"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	StatusWaiting  = "waiting"
	StatusRunning  = "running"
	StatusFinished = "finished"
	StatusError    = "error"
)

var emptyLinkPatternStr = `(^data:)|(^tel:)|(^mailto:)|(about:blank)|(javascript:)`
var emptyLinkPattern = regexp.MustCompile(emptyLinkPatternStr)

const (
	// URLTaskStatusInit 任务状态初始值, 0
	URLTaskStatusInit = iota
	// URLTaskStatusPending 从队列中取出, 未出结果时的状态.
	URLTaskStatusPending
	// URLTaskStatusSuccess 任务状态成功, 2
	URLTaskStatusSuccess
	// URLTaskStatusFailed 任务状态失败(404), 3
	URLTaskStatusFailed
)

const (
	URLTypePage int = iota
	URLTypeAsset
)

// SpecialCharsMap 查询参数中的特殊字符
var SpecialCharsMap = map[string]string{
	"\\": "xg",
	":":  "mh",
	"*":  "xh",
	"?":  "wh",
	"<":  "xy",
	">":  "dy",
	"|":  "sx",
	" ":  "kg",
}

// 以如下格式结尾的路径才是可以直接以静态路径访问的.
// 其他如.php, .jsp, .asp等如果nginx中没有相应的处理方法, 无法直接展示.
var htmlURLPatternStr = `(\.(html)|(htm)|(xhtml)|(shtml)|(xml))$`
var htmlURLPattern = regexp.MustCompile(htmlURLPatternStr)

var cssAssetURLPatternStr = `url\(\'(.*?)\'\)|url\(\"(.*?)\"\)|url\((.*?)\)`
var cssAssetURLPattern = regexp.MustCompile(cssAssetURLPatternStr)

var ErrDuplicate = errors.New("duplicate")

type URLRecord struct {
	URL         string `json:"url"`
	Refer       string `json:"refer"`
	Depth       int
	URLType     int
	FailedTimes int
	Status      int
}

const (
	CrawlerTypeCollect  = "collect"
	CrawlerTypeSitemap  = "sitemap"
	CrawlerType404      = "404"
	CrawlerTypeOutLink  = "outlink"
	CrawlerTypeDownload = "download"
)

type Crawler struct {
	Id               int `json:"id"`
	ctx              context.Context
	Cancel           context.CancelFunc `json:"-"`
	Type             string             `json:"type"` // 支持的type：collect，需要task；sitemap，生成sitemap；404，检测404页面；download，整站下载
	PageWorkerCount  int                `json:"-"`
	AssetWorkerCount int                `json:"-"`
	PageQueue        chan *URLRecord    `json:"-"`
	AssetQueue       chan *URLRecord    `json:"-"`
	LinksPool        *sync.Map          `json:"-"`
	LinksMutex       *sync.Mutex        `json:"-"`
	MainSite         string             `json:"-"`
	MaxRetryTimes    int                `json:"-"`

	Render bool   `json:"render"`
	Prefix string `json:"prefix"` // 只采集已xx开头的地址

	sitemapFile *SitemapGenerator

	Domain       string `json:"domain"`
	SavePath     string `json:"save_path"`
	Status       string `json:"status"` // empty,waiting,running, finished, error
	ErrMsg       string `json:"err_msg"`
	Total        int    `json:"total"`
	Finished     int    `json:"finished"`
	Notfound     int    `json:"notfound"`
	Outlink      int    `json:"outlink"`
	Single       bool   `json:"single"`
	ArticleCount int    `json:"article_count"`

	gRWLock *sync.RWMutex

	OnFinished func() `json:"-"`
	lastActive int64
	IsActive   bool `json:"is_active"`
	canceled   bool
}

var RunningCrawler *Crawler

func NewCrawler(crawlerType string, startPage string, savePath string) (*Crawler, error) {
	if RunningCrawler != nil {
		RunningCrawler.Stop()
	}

	urlObj, err := url.Parse(startPage)
	if err != nil {
		log.Printf("解析起始地址失败: url: %s, %s", startPage, err.Error())
		return nil, err
	}
	if crawlerType != CrawlerTypeCollect {
		if crawlerType == CrawlerTypeDownload {
			_, err = os.Stat(savePath)
			if err != nil {
				log.Errorf("存储地址不存在")
				return nil, err
			}
		} else {
			// 检测上级目录
			_, err = os.Stat(filepath.Dir(savePath))
			if err != nil {
				log.Errorf("存储地址不存在")
				return nil, err
			}
		}
	}
	log.SetLevel(log.INFO)

	ctx, cancelFunc := context.WithCancel(context.Background())

	crawler := &Crawler{
		ctx:              ctx,
		Cancel:           cancelFunc,
		Type:             crawlerType,
		PageWorkerCount:  5,
		AssetWorkerCount: 5,
		SavePath:         savePath,
		PageQueue:        make(chan *URLRecord, 500000),
		AssetQueue:       make(chan *URLRecord, 500000),
		LinksPool:        &sync.Map{},
		LinksMutex:       &sync.Mutex{},
		Domain:           startPage,
		MaxRetryTimes:    3,
		IsActive:         true,
		lastActive:       time.Now().Unix(),
		gRWLock:          new(sync.RWMutex),
	}
	mainSite := urlObj.Host // Host成员带端口.
	crawler.MainSite = mainSite

	err = crawler.LoadTaskQueue()
	if err != nil {
		log.Errorf("加载任务队列失败: %s", err.Error())
		cancelFunc()
		return nil, err
	}
	crawler.Id = int(time.Now().Unix())

	if crawlerType == CrawlerTypeSitemap {
		crawler.sitemapFile = NewSitemapGenerator("txt", crawler.SavePath, false)
	}

	RunningCrawler = crawler

	return crawler, nil
}

func (crawler *Crawler) isCanceled() bool {
	select {
	case <-crawler.ctx.Done():
		return true
	default:
		return false
	}
}

// Start 启动n个工作协程
func (crawler *Crawler) Start() {
	req := &URLRecord{
		URL:         crawler.Domain,
		URLType:     URLTypePage,
		Refer:       "",
		Depth:       1,
		FailedTimes: 0,
	}
	crawler.EnqueuePage(req)

	//todo 加 waitGroup
	for i := 0; i < crawler.PageWorkerCount; i++ {
		go crawler.GetHTMLPage(i)
	}
	// only download need to work with assets
	if crawler.Type == CrawlerTypeDownload {
		for i := 0; i < crawler.AssetWorkerCount; i++ {
			go crawler.GetStaticAsset(i)
		}
	}
	//检查活动
	go crawler.CheckProcess()
}

func (crawler *Crawler) Stop() {
	if !crawler.IsActive {
		return
	}

	crawler.LinksMutex.Lock()
	crawler.IsActive = false
	//停止
	//time.Sleep(200 * time.Millisecond)
	close(crawler.AssetQueue)
	close(crawler.PageQueue)
	crawler.LinksMutex.Unlock()

	if crawler.sitemapFile != nil {
		_ = crawler.sitemapFile.Save()
	}

	log.Infof("任务完成", crawler.Domain)
	//开始执行抓取任务
	if crawler.OnFinished != nil && !crawler.canceled {
		crawler.OnFinished()
	}

	RunningCrawler = nil
}

func (crawler *Crawler) CheckProcess() {
	for {
		//log.Println(crawler.IsActive, crawler.canceled, crawler.lastActive, time.Now().Unix())
		time.Sleep(1 * time.Second)
		// 30秒不活动，则咔嚓掉
		isCanceled := crawler.isCanceled()
		if isCanceled {
			crawler.canceled = true
			log.Infof("任务终止")
			crawler.Stop()
			break
		}
		if crawler.IsActive && crawler.lastActive < time.Now().Unix()-20 {
			log.Infof("任务完成")
			crawler.Stop()
			break
		}
	}
}

// LoadTaskQueue 初始化任务队列, 读取数据库中的`PageTask`与`AssetTask`表,
// 将其中缓存的任务加载到任务队列中
func (crawler *Crawler) LoadTaskQueue() (err error) {
	log.Info("初始化任务队列")
	pageTasks, err := crawler.QueryUnfinishedPageTasks()

	if err != nil {
		log.Errorf("获取页面任务失败: %s", err.Error())
		return
	}

	log.Debugf("获取页面队列任务数量: %d", len(pageTasks))
	for _, task := range pageTasks {
		crawler.PageQueue <- task
	}

	if crawler.Type == CrawlerTypeDownload {
		assetTasks, err2 := crawler.QueryUnfinishedAssetTasks()

		if err != nil {
			err = err2
			log.Errorf("获取页面任务失败: %s", err.Error())
			return
		}

		log.Debugf("获取静态资源队列任务数量: %d", len(pageTasks))
		for _, task := range assetTasks {
			crawler.AssetQueue <- task
		}
	}
	crawler.Total = len(crawler.PageQueue) + len(crawler.AssetQueue)
	log.Infof("初始化任务队列完成, 页面任务数量: %d, 静态资源任务数量: %d", len(crawler.PageQueue), len(crawler.AssetQueue))
	return
}

func (crawler *Crawler) QueryUnfinishedPageTasks() (tasks []*URLRecord, err error) {
	crawler.LinksMutex.Lock()
	defer crawler.LinksMutex.Unlock()

	return crawler.queryUnfinishedTasks(URLTypePage)
}

func (crawler *Crawler) QueryUnfinishedAssetTasks() (tasks []*URLRecord, err error) {
	crawler.LinksMutex.Lock()
	defer crawler.LinksMutex.Unlock()

	return crawler.queryUnfinishedTasks(URLTypeAsset)
}

func (crawler *Crawler) queryUnfinishedTasks(urlType int) (tasks []*URLRecord, err error) {
	tasks = []*URLRecord{}
	crawler.LinksPool.Range(func(key, value interface{}) bool {
		record, ok := value.(*URLRecord)
		if ok && record.URLType == urlType && (record.Status == URLTaskStatusInit || record.Status == URLTaskStatusPending) {
			tasks = append(tasks, record)
		}
		return true
	})

	return
}

// EnqueuePage 页面任务入队列.
// 入队列前查询数据库记录, 如已有记录则不再接受.
// 已进入队列的任务, 必定已经存在记录, 但不一定能成功下载.
// 由于队列长度有限, 这里可能会阻塞, 最可能发生死锁
// 每个page worker在解析页面时, 会将页面中的链接全部入队列.
// 如果此时队列已满, page worker就会阻塞, 当所有worker都阻塞到这里时, 程序就无法继续执行.
func (crawler *Crawler) EnqueuePage(req *URLRecord) {
	//防止出错，任务停止了就不写队列了
	if !crawler.IsActive {
		log.Infof("通道已关闭")
		return
	}

	var err error
	err = crawler.AddOrUpdateURLRecord(req)
	if err != nil {
		if errors.Is(err, ErrDuplicate) {
			//重复，跳过
			return
		}
		log.Errorf("添加(更新)页面任务url记录失败, req: %s, err: %s", req.URL, err.Error())
		return
	}

	crawler.Total++
	crawler.PageQueue <- req

	return
}

// EnqueueAsset 页面任务入队列.
// 入队列前查询数据库记录, 如已有记录则不再接受.
func (crawler *Crawler) EnqueueAsset(req *URLRecord) {
	if crawler.Type != CrawlerTypeDownload {
		return
	}
	var err error
	//防止出错，任务停止了就不写队列了
	if !crawler.IsActive {
		log.Infof("通道已关闭")
		return
	}

	err = crawler.AddOrUpdateURLRecord(req)
	if err != nil {
		if errors.Is(err, ErrDuplicate) {
			//重复，跳过
			return
		}
		log.Errorf("添加(更新)静态资源任务url记录失败, req: %s, err: %s", req.URL, err.Error())
		return
	}

	// 由于队列长度有限, 这里可能会阻塞
	crawler.AssetQueue <- req
	crawler.Total++

	return
}

func (crawler *Crawler) SafeFile(v ...interface{}) {
	logFile, err := os.OpenFile(crawler.SavePath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0766)
	if nil != err {
		//打开失败，不做记录
		return
	}
	defer logFile.Close()
	crawler.gRWLock.Lock()
	logFile.WriteString(fmt.Sprintln(v...))
	crawler.gRWLock.Unlock()
}

func (crawler *Crawler) AddOrUpdateURLRecord(task *URLRecord) (err error) {
	crawler.LinksMutex.Lock()
	defer crawler.LinksMutex.Unlock()
	key := utils.GetMd5String(task.URL, false, false)
	value, ok := crawler.LinksPool.Load(key)
	if !ok {
		crawler.LinksPool.Store(key, task)
		return
	}

	_, ok = value.(*URLRecord)
	if !ok {
		crawler.LinksPool.Store(key, task)
		return
	}

	//任务中，同一个url，不重复入库
	return ErrDuplicate
}

func (crawler *Crawler) isExistInURLRecord(url string) bool {
	key := utils.GetMd5String(url, false, false)
	_, ok := crawler.LinksPool.Load(key)

	return ok
}

func (crawler *Crawler) UpdateURLRecordStatus(url string, status int) (err error) {
	crawler.LinksMutex.Lock()
	defer crawler.LinksMutex.Unlock()
	key := utils.GetMd5String(url, false, false)
	value, ok := crawler.LinksPool.Load(key)
	if !ok {
		err = errors.New("记录不存在")
		return
	}

	record, ok := value.(*URLRecord)
	if !ok {
		err = errors.New("记录出错")
		return
	}

	record.Status = status
	crawler.LinksPool.Store(key, record)
	return
}

// getAndRead 发起请求获取页面或静态资源, 返回响应体内容.
func (crawler *Crawler) getAndRead(req *URLRecord) (body []byte, header http.Header, err error) {
	err = crawler.UpdateURLRecordStatus(req.URL, URLTaskStatusPending)
	if err != nil {
		log.Infof("更新任务队列记录失败: req: %s, error: %s", req.URL, err.Error())
		return
	}

	if req.FailedTimes > crawler.MaxRetryTimes {
		log.Infof("失败次数过多, 不再尝试: req: %s", req.URL)
		return
	}
	if req.URLType == URLTypePage && crawler.Single && 1 < req.Depth {
		log.Infof("当前页面已达到最大深度, 不再抓取: req: %s", req.URL)
		return
	}

	if crawler.Render && req.URLType == URLTypePage {
		var content string
		content, err = ChromeDPGetArticle(req.URL)
		if err != nil {
			log.Errorf("请求失败, 重新入队列: req: %s, error: %s", req.URL, err.Error())
			req.FailedTimes++
			if req.URLType == URLTypePage {
				crawler.EnqueuePage(req)
			}
			return
		}
		header = http.Header{}
		header.Set("Content-Type", "text/html")
		body = []byte(content)
	} else {
		var resp *http.Response
		resp, err = getURL(req.URL, req.Refer)
		if err != nil {
			log.Errorf("请求失败, 重新入队列: req: %s, error: %s", req.URL, err.Error())
			req.FailedTimes++
			if req.URLType == URLTypePage {
				crawler.EnqueuePage(req)
			}
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			crawler.Notfound++
			if crawler.Type == CrawlerType404 {
				crawler.SafeFile(req.URL, resp.StatusCode)
			}
			// 抓取失败一般是5xx或403, 405等, 出现404基本上就没有重试的意义了, 可以直接放弃
			err = crawler.UpdateURLRecordStatus(req.URL, URLTaskStatusFailed)
			log.Infof("页面404等错误: req: %s", req.URL)
			if err != nil {
				log.Errorf("更新任务记录状态失败: req: %s, error: %s", req.URL, err.Error())
			}
			err = errors.New(fmt.Sprintf("页面错误：%d", resp.StatusCode))
			return
		}

		header = resp.Header
		body, err = io.ReadAll(resp.Body)
	}

	return
}

// GetHTMLPage 工作协程, 从队列中获取任务, 请求html页面并解析
func (crawler *Crawler) GetHTMLPage(num int) {
	for req := range crawler.PageQueue {
		if !crawler.IsActive {
			log.Infof("通道已关闭")
			continue
		}
		crawler.lastActive = time.Now().Unix()

		log.Infof("取得页面任务: %s", req.URL)

		respBody, respHeader, err := crawler.getAndRead(req)
		if err != nil {
			log.Errorf("获取页面错误: req: %s, error: %s", req.URL, err.Error())
			continue
		}

		fileDir, fileName, err := TransToLocalPath(crawler.MainSite, req.URL, URLTypePage)
		if err != nil {
			log.Errorf("转换为本地链接失败: req: %s, error: %s", req.URL, err.Error())
			continue
		}

		var fileContent []byte

		//尝试判断是否是html,不是的话，不需要wrap
		field, exist := respHeader["Content-Type"]
		if exist && !strings.Contains(field[0], "text/html") && !strings.Contains(field[0], "text/plain") && len(respBody) > 0 && respBody[0] != '<' {
			if crawler.Type == CrawlerTypeDownload {
				//是一个图片,视频，音频，压缩包、文档，无需wrap，
				//因为开始的时候，并不知道文件格式，所以这里增加一个中转页面
				realLink := strings.TrimSuffix(fileName, ".html") // .html 是转换的时候加上去的
				if filetype.IsImage(respBody) {
					fileContent = []byte(fmt.Sprintf("<html><head><meta charset=\"utf-8\"></head><body style=\"text-align:center;\"><img src=\"%s\" alt=\"%s\"></body></html>", realLink, filepath.Base(realLink)))
				} else {
					fileContent = []byte(fmt.Sprintf("<html><head><meta charset=\"utf-8\"></head><body><a href=\"%s\" id=\"link\">%s</a><script>document.getElementById(\"link\").click();</script></body></html>", realLink, filepath.Base(realLink)))
				}
				err = WriteToLocalFile(crawler.SavePath, fileDir, realLink, respBody)
				if err != nil {
					if err.Error() == "equal" {
						//相同，跳过
						log.Debugf("跳过未更新文件: req: %s", req.URL)
						err = crawler.UpdateURLRecordStatus(req.URL, URLTaskStatusSuccess)
						continue
					}
					log.Errorf("写入文件失败: req: %v, error: %s", req, err.Error())
					continue
				}
			} else {
				fileContent = respBody
			}
		} else if strings.HasSuffix(fileName, ".xml") {
			// xml 文件不需要转换
			fileContent = []byte(crawler.ParseXmlAssets(string(respBody), req))
		} else {
			// 编码处理
			contentType := respHeader.Get("Content-Type")
			charsetName, err := utils.GetPageCharset(string(respBody), contentType)
			if err != nil {
				log.Errorf("获取页面编码失败: req: %s, error: %s", req.URL, err.Error())
				continue
			}
			charsetName = strings.ToLower(charsetName)
			log.Debugf("当前页面编码: %s, req: %s", charsetName, req.URL)
			charset, exist := utils.CharsetMap[charsetName]
			if !exist {
				log.Debugf("未找到匹配的编码: req: %s, error: %s", req.URL, charsetName)
			}
			utf8Content, err := utils.DecodeToUTF8(respBody, charset)
			if err != nil {
				log.Errorf("页面解码失败: req: %s, error: %s", req.URL, err.Error())
				continue
			}
			utf8Reader := bytes.NewReader(utf8Content)
			htmlDom, err := goquery.NewDocumentFromReader(utf8Reader)
			if err != nil {
				log.Errorf("生成dom树失败: req: %s, error: %s", req.URL, err.Error())
				continue
			}

			log.Debugf("准备进行页面解析: req: %s", req.URL)

			crawler.ParseLinkingPages(htmlDom, req)
			crawler.ParseLinkingAssets(htmlDom, req)

			log.Debugf("准备写入本地文件: req: %s", req.URL)

			htmlString, err := htmlDom.Html()
			if err != nil {
				log.Errorf("获取页面Html()值失败: req: %s, error: %s", req.URL, err.Error())
				continue
			}
			htmlString = utils.ReplaceHTMLCharacterEntities(htmlString, charset)
			if crawler.Type == CrawlerTypeDownload {
				fileContent, err = utils.EncodeFromUTF8([]byte(htmlString), charset)
				if err != nil {
					log.Errorf("页面编码失败: req: %s, error: %s", req.URL, err.Error())
					continue
				}
			} else {
				if crawler.Type == CrawlerTypeSitemap {
					crawler.sitemapFile.AddLoc(req.URL, time.Now().Format("2006-01-02"))
				}
			}
		}

		if crawler.Type == CrawlerTypeDownload {
			err = WriteToLocalFile(crawler.SavePath, fileDir, fileName, fileContent)
			if err != nil {
				if err.Error() == "equal" {
					//相同，跳过
					log.Debugf("跳过未更新文件: req: %s", req.URL)
					err = crawler.UpdateURLRecordStatus(req.URL, URLTaskStatusSuccess)
					continue
				}
				log.Errorf("写入文件失败: req: %v, error: %s", req, err.Error())
				continue
			}

			log.Debugf("页面任务写入本地文件成功: req: %s", req.URL)
		}
		err = crawler.UpdateURLRecordStatus(req.URL, URLTaskStatusSuccess)

		if err != nil {
			log.Errorf("更新任务记录状态失败: req: %s, error: %s", req.URL, err.Error())
			continue
		}

		crawler.Finished++

		log.Debugf("页面任务完成: req: %s", req.URL)
	}
}

// GetStaticAsset 工作协程, 从队列中获取任务, 获取静态资源并存储
func (crawler *Crawler) GetStaticAsset(num int) {
	for req := range crawler.AssetQueue {
		if !crawler.IsActive {
			log.Infof("通道已关闭")
			continue
		}
		crawler.lastActive = time.Now().Unix()

		log.Infof("取得静态资源任务: %#v", req)

		respBody, respHeader, err := crawler.getAndRead(req)
		if err != nil {
			log.Errorf("获取页面错误: req: %s, error: %s", req.URL, err.Error())
			continue
		}
		// 如果是css文件, 解析其中的链接, 否则直接存储.
		field, exist := respHeader["Content-Type"]
		if exist {
			if field[0] == "text/css" {
				respBody, err = crawler.parseCSSFile(respBody, req)
				if err != nil {
					log.Errorf("解析css文件失败: req: %s, error: %s", req.URL, err.Error())
					continue
				}
			}
			if strings.Contains(field[0], "text/html") {
				log.Errorf("它不是一个assets: req: %s", req.URL)
				continue
			}
			// 如果是TXT文件, 一些txt形式的sitemap，解析其中的链接
			if field[0] == "text/plain" {
				respBody, err = crawler.parseTxtFile(respBody, req)
				if err != nil {
					log.Errorf("解析txt文件失败: req: %s, error: %s", req.URL, err.Error())
					continue
				}
			}
		}

		fileDir, fileName, err := TransToLocalPath(crawler.MainSite, req.URL, URLTypeAsset)
		if err != nil {
			log.Errorf("转换为本地链接失败: req: %s, error: %s", req.URL, err.Error())
			continue
		}

		err = WriteToLocalFile(crawler.SavePath, fileDir, fileName, respBody)
		if err != nil {
			if err.Error() == "equal" {
				//相同，跳过
				log.Debugf("跳过未更新文件: req: %s", req.URL)
				err = crawler.UpdateURLRecordStatus(req.URL, URLTaskStatusSuccess)
				continue
			}
			log.Errorf("写入文件失败: req: %v, error: %s", req, err.Error())
			continue
		}
		log.Debugf("静态资源任务写入本地文件成功: req: %s", req.URL)

		err = crawler.UpdateURLRecordStatus(req.URL, URLTaskStatusSuccess)

		if err != nil {
			log.Errorf("更新任务记录状态失败: req: %s, error: %s", req.URL, err.Error())
			continue
		}

		crawler.Finished++

		log.Debugf("静态资源任务完成: req: %s", req.URL)
	}
}

// ParseXmlAssets 增加支持 xml 中的 href
func (crawler *Crawler) ParseXmlAssets(content string, req *URLRecord) string {
	reg := regexp.MustCompile(`href=["'](.*?)["']`)
	matches := reg.FindAllStringSubmatch(content, -1)
	if len(matches) > 0 {
		for _, match := range matches {
			subURL := match[1]
			fullURL, fullURLWithoutFrag := joinURL(req.URL, subURL)
			if !crawler.URLFilter(fullURL, URLTypeAsset) {
				continue
			}
			if crawler.Type == CrawlerTypeDownload {
				localLink, err := TransToLocalLink(crawler.MainSite, fullURL, URLTypeAsset)
				if err != nil {
					continue
				}
				fullLink, _ := joinURL(req.URL, localLink)
				content = strings.ReplaceAll(content, match[1], fullLink)
			}
			// 新任务入队列
			req := &URLRecord{
				URL:     fullURLWithoutFrag,
				URLType: URLTypeAsset,
				Refer:   req.URL,
				Depth:   req.Depth + 1,
			}
			crawler.EnqueueAsset(req)
		}
	}
	//匹配 loc，是html
	reg = regexp.MustCompile(`(?is)<loc>(.*?)</loc>`)
	matches = reg.FindAllStringSubmatch(content, -1)
	if len(matches) > 0 {
		for _, match := range matches {
			subURL := strings.TrimSpace(match[1])
			fullURL, fullURLWithoutFrag := joinURL(req.URL, subURL)
			if !crawler.URLFilter(fullURL, URLTypePage) {
				continue
			}

			if crawler.Type == CrawlerTypeDownload {
				localLink, err := TransToLocalLink(crawler.MainSite, fullURL, URLTypePage)
				if err != nil {
					continue
				}
				fullLink, _ := joinURL(req.URL, localLink)
				content = strings.ReplaceAll(content, match[1], fullLink)
			}
			// 新任务入队列
			req := &URLRecord{
				URL:     fullURLWithoutFrag,
				URLType: URLTypePage,
				Refer:   req.URL,
				Depth:   req.Depth + 1,
			}
			crawler.EnqueuePage(req)
		}
	}

	return content
}

// parseCSSFile 解析css文件中的链接, 获取资源并修改其引用路径.
// css中可能包含url属性,或者是background-image属性的引用路径,
// 格式可能为url('./bg.jpg'), url("./bg.jpg"), url(bg.jpg)
func (crawler *Crawler) parseCSSFile(content []byte, req *URLRecord) (newContent []byte, err error) {
	fileStr := string(content)
	// FindAllStringSubmatch返回值为切片, 是所有匹配到的字符串集合.
	// 其成员也是切片, 此切片类似于FindStringSubmatch()的结果, 表示分组的匹配情况.
	matchedArray := cssAssetURLPattern.FindAllStringSubmatch(fileStr, -1)
	for _, matchedItem := range matchedArray {
		for _, matchedURL := range matchedItem[1:] {
			if matchedURL == "" || emptyLinkPattern.MatchString(matchedURL) {
				continue
			}

			fullURL, fullURLWithoutFrag := joinURL(req.URL, matchedURL)
			if !crawler.URLFilter(fullURL, URLTypeAsset) {
				return
			}
			localLink, err := TransToLocalLink(crawler.MainSite, fullURL, URLTypeAsset)
			if err != nil {
				continue
			}
			fileStr = strings.Replace(fileStr, matchedURL, localLink, -1)
			// 新任务入队列
			req := &URLRecord{
				URL:     fullURLWithoutFrag,
				URLType: URLTypeAsset,
				Refer:   req.URL,
				Depth:   req.Depth + 1,
			}
			crawler.EnqueueAsset(req)
		}
	}
	newContent = []byte(fileStr)
	return
}

// parseTxtFile 解析text 只解析 http开头的链接
func (crawler *Crawler) parseTxtFile(content []byte, req *URLRecord) (newContent []byte, err error) {
	fileStr := string(content)
	// FindAllStringSubmatch返回值为切片, 是所有匹配到的字符串集合.
	// 其成员也是切片, 此切片类似于FindStringSubmatch()的结果, 表示分组的匹配情况.
	// (https?|ftp|file)://[-A-Za-z0-9+&@#/%?=~_|!:,.;]+[-A-Za-z0-9+&@#/%=~_|]
	reg := regexp.MustCompile(`https?://([^\s]+)?`)
	matchedArray := reg.FindAllStringSubmatch(fileStr, -1)

	for _, matchedItem := range matchedArray {
		subURL := strings.TrimSpace(matchedItem[0])
		fullURL, fullURLWithoutFrag := joinURL(req.URL, subURL)
		if !crawler.URLFilter(fullURL, URLTypePage) {
			continue
		}

		localLink, err := TransToLocalLink(crawler.MainSite, fullURL, URLTypePage)
		if err != nil {
			continue
		}

		fullLink, _ := joinURL(req.URL, localLink)
		fileStr = strings.ReplaceAll(fileStr, matchedItem[0], fullLink)

		// 新任务入队列，解析的是html
		if !htmlURLPattern.MatchString(localLink) && !strings.HasSuffix(localLink, "/") && strings.Contains(filepath.Base(localLink), ".") {
			req := &URLRecord{
				URL:     fullURLWithoutFrag,
				URLType: URLTypeAsset,
				Refer:   req.URL,
				Depth:   req.Depth + 1,
			}
			crawler.EnqueueAsset(req)
		} else {
			// 新任务入队列
			req := &URLRecord{
				URL:     fullURLWithoutFrag,
				URLType: URLTypePage,
				Refer:   req.URL,
				Depth:   req.Depth + 1,
			}
			crawler.EnqueuePage(req)
		}
	}
	newContent = []byte(fileStr)
	return
}

// URLFilter ...
func (crawler *Crawler) URLFilter(fullURL string, urlType int) (boolean bool) {
	urlObj, err := url.Parse(fullURL)
	if err != nil {
		log.Errorf("解析地址失败: url: %s, %s", fullURL, err.Error())
		return
	}
	if urlType == URLTypePage && urlObj.Host != crawler.MainSite {
		log.Debugf("不抓取站外页面: %s", fullURL)
		// 记录到 Outlink
		if crawler.Type == CrawlerTypeOutLink {

		}
		return
	}

	return true
}

func (crawler *Crawler) CheckOutlink(fullURL string) (boolean bool) {
	if crawler.Type != CrawlerTypeOutLink {
		return
	}
	urlObj, err := url.Parse(fullURL)
	if err != nil {
		log.Errorf("解析地址失败: url: %s, %s", fullURL, err.Error())
		return
	}
	if urlObj.Host != crawler.MainSite {
		return true
	}

	return
}

// ParseLinkingPages 解析并改写页面中的页面链接, 包括a, iframe等元素
func (crawler *Crawler) ParseLinkingPages(htmlDom *goquery.Document, req *URLRecord) {
	aList := htmlDom.Find("a")
	crawler.parseLinkingPages(aList, req, "href")
}

// ParseLinkingAssets 解析并改写页面中的静态资源链接, 包括js, css, img等元素，增加支持 xml 中的 href
func (crawler *Crawler) ParseLinkingAssets(htmlDom *goquery.Document, req *URLRecord) {
	if crawler.Type != CrawlerTypeDownload {
		return
	}
	linkList := htmlDom.Find("[href]")
	crawler.parseLinkingAssets(linkList, req, "href")

	scriptList := htmlDom.Find("[src]")
	crawler.parseLinkingAssets(scriptList, req, "src")
}

func (crawler *Crawler) parseLinkingAssets(nodeList *goquery.Selection, req *URLRecord, attrName string) {
	// nodeList.Nodes 对象表示当前选择器中包含的元素
	nodeList.Each(func(i int, nodeItem *goquery.Selection) {
		//不对 canonical 标签进行处理
		if attrName == "href" {
			nodeName := nodeItem.Nodes[0].Data
			if nodeName == "a" {
				// a标签的跳过
				return
			}
			rel, exists := nodeItem.Attr("rel")
			if exists && rel == "canonical" {
				return
			}
		}
		subURL, exist := nodeItem.Attr(attrName)
		if !exist || emptyLinkPattern.MatchString(subURL) {
			return
		}

		fullURL, fullURLWithoutFrag := joinURL(req.URL, subURL)
		if !crawler.URLFilter(fullURL, URLTypeAsset) {
			return
		}
		localLink, err := TransToLocalLink(crawler.MainSite, fullURL, URLTypeAsset)
		if err != nil {
			return
		}
		nodeItem.SetAttr(attrName, localLink)

		// 新任务入队列
		req := &URLRecord{
			URL:     fullURLWithoutFrag,
			URLType: URLTypeAsset,
			Refer:   req.URL,
			Depth:   req.Depth + 1,
		}
		crawler.EnqueueAsset(req)
	})
}

// parseLinkingPages 遍历选中节点, 解析链接入库, 同时修改节点的链接属性.
func (crawler *Crawler) parseLinkingPages(nodeList *goquery.Selection, req *URLRecord, attrName string) {
	// nodeList.Nodes 对象表示当前选择器中包含的元素
	nodeList.Each(func(i int, nodeItem *goquery.Selection) {
		subURL, exist := nodeItem.Attr(attrName)
		if !exist || emptyLinkPattern.MatchString(subURL) {
			return
		}
		fullURL, fullURLWithoutFrag := joinURL(req.URL, subURL)
		if crawler.CheckOutlink(fullURL) {
			crawler.Outlink++
			crawler.SafeFile(fullURLWithoutFrag, req.URL)
		}
		if !crawler.URLFilter(fullURL, URLTypePage) {
			return
		}
		if crawler.Single && 1 < req.Depth+1 {
			log.Infof("当前页面已达到最大深度, 不再解析新页面: %s", req.URL)
			return
		}

		localLink, err := TransToLocalLink(crawler.MainSite, fullURL, URLTypePage)
		if err != nil {
			return
		}
		if crawler.Type == CrawlerTypeDownload {
			nodeItem.SetAttr(attrName, localLink)
		}

		//如果不是html资源，则添加到access
		if !htmlURLPattern.MatchString(localLink) && !strings.HasSuffix(localLink, "/") {
			req2 := &URLRecord{
				URL:     fullURLWithoutFrag,
				URLType: URLTypeAsset,
				Refer:   req.URL,
				Depth:   req.Depth + 1,
			}
			crawler.EnqueueAsset(req2)
		} else {
			// 新任务入队列
			req2 := &URLRecord{
				URL:     fullURLWithoutFrag,
				URLType: URLTypePage,
				Refer:   req.URL,
				Depth:   req.Depth + 1,
			}
			crawler.EnqueuePage(req2)
		}
	})
}

func getURL(url, refer string) (resp *http.Response, err error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", utils.DefaultUserAgent)
	req.Header.Set("Referer", refer)

	resp, err = client.Do(req)
	if err != nil {
		return
	}
	return
}

// TransToLocalLink ...
// @return: localLink 本地链接, 用于写入本地html文档中的link/script/img/a等标签的链接属性, 格式为以斜线/起始的根路径.
func TransToLocalLink(mainSite string, fullURL string, urlType int) (localLink string, err error) {
	// 对于域名为host的url, 资源存放目录为output根目录, 而不是域名文件夹. 默认不设置主host
	urlObj, err := url.Parse(fullURL)
	if err != nil {
		log.Errorf("解析URL出错: %s", err.Error())
		return
	}
	originHost := urlObj.Host
	originPath := urlObj.Path

	localLink = originPath
	if urlType == URLTypePage {
		localLink = transToLocalLinkForPage(urlObj)
	} else {
		localLink = transToLocalLinkForAsset(urlObj)
	}

	//以 / 结尾的，始终保持斜杠结尾
	if strings.HasSuffix(fullURL, "/") && strings.HasSuffix(localLink, "index.html") {
		localLink = strings.TrimSuffix(localLink, "index.html")
	}
	// fragment
	if urlObj.Fragment != "" {
		localLink += "#" + urlObj.Fragment
	}

	// 如果该url就是当前站点域名下的，那么无需新建域名目录存放.
	// 如果是其他站点的(需要事先开启允许下载其他站点静态资源的配置),
	// 则要将资源存放在以站点域名为名的目录下, 路径中仍然需要保留域名部分.
	if originHost != mainSite {
		host := originHost
		// 有时originHost中可能包含端口, 冒号需要转义.
		host = strings.Replace(host, ":", SpecialCharsMap[":"], -1)
		localLink = "/" + host + localLink
	}

	return
}

func transToLocalLinkForPage(urlObj *url.URL) (localLink string) {
	originPath := urlObj.Path
	originQuery := urlObj.RawQuery

	localLink = originPath

	// 如果path为空
	if localLink == "" {
		localLink = "index.html"
	}
	// 如果path以/结尾
	boolean := strings.HasSuffix(localLink, "/")
	if boolean && originQuery == "" {
		localLink += "index.html"
	}

	// 替换query参数中的特殊字符
	if originQuery != "" {
		queryStr := originQuery
		for key, val := range SpecialCharsMap {
			queryStr = strings.Replace(queryStr, key, val, -1)
		}
		localLink = localLink + SpecialCharsMap["?"] + queryStr
	}

	// 如果是不支持的页面后缀, 如.php, .jsp, .asp等
	// 注意此时localLink可能是拼接过query的字符串.
	// a 标签下也会包含静态资源，因此，静态资源也需要支持
	if _, ok := utils.WhiteExts[strings.ToLower(filepath.Ext(localLink))]; !ok {
		localLink += ".html"
	}

	return
}

func transToLocalLinkForAsset(urlObj *url.URL) (localLink string) {
	originPath := urlObj.Path

	localLink = originPath

	// 如果path以/结尾
	boolean := strings.HasSuffix(localLink, "/")
	if boolean {
		localLink += "index"
	}

	return
}

// TransToLocalPath ...
// @return: 返回本地路径与文件名称, 用于写入本地文件
func TransToLocalPath(mainSite string, fullURL string, urlType int) (fileDir string, fileName string, err error) {
	localLink, err := TransToLocalLink(mainSite, fullURL, urlType)

	// 如果是站外资源, local_link可能为/www.xxx.com/static/x.jpg,
	// 但我们需要的存储目录是相对路径, 所以需要事先将链接起始的斜线/移除, 作为相对路径.
	if strings.HasPrefix(localLink, "/") {
		localLink = localLink[1:]
	}
	//以 / 结尾的，添加index.html
	if strings.HasSuffix(localLink, "/") {
		localLink += "index.html"
	}

	//可能没有文件名
	if localLink == "" {
		localLink = "index.html"
	}

	fileDir = path.Dir(localLink)
	fileName = path.Base(localLink)
	return
}

// WriteToLocalFile ...
func WriteToLocalFile(baseDir string, fileDir string, fileName string, fileContent []byte) (err error) {
	fileDir = path.Join(baseDir, fileDir)
	err = os.MkdirAll(fileDir, os.ModePerm)
	if err != nil {
		log.Errorf("创建文件夹失败: %s", err.Error())
		return err
	}
	filePath := path.Join(fileDir, fileName)

	existsContent, err := os.ReadFile(filePath)
	if err == nil {
		//读取成功
		// 验证文件是否已经存在并相同，如果是，则不写入
		fileMd5 := utils.GetMd5(fileContent, false, false)
		existMd5 := utils.GetMd5(existsContent, false, false)
		if fileMd5 == existMd5 {
			//一致，表示文件没有更新，不需要重新写入
			return errors.New("equal")
		}
	}

	file, err := os.Create(filePath)
	defer file.Close()

	_, err = file.Write(fileContent)
	if err != nil {
		log.Errorf("写入文件失败: %s", err.Error())
	}
	return
}

func (crawler *Crawler) ChromeDPGet(link string) (string, error) {
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

// 注意，第三方url的host不做覆盖
func joinURL(baseURL, subURL string) (fullURL, fullURLWithoutFrag string) {
	baseURL = strings.TrimSpace(baseURL)
	subURL = strings.TrimSpace(subURL)
	baseURLObj, err := url.Parse(baseURL)
	if err != nil {
		return
	}
	fullURLObj := baseURLObj
	subURLObj, err := url.Parse(subURL)
	if err == nil {
		fullURLObj = baseURLObj.ResolveReference(subURLObj)
	}
	fullURL = fullURLObj.String()
	fullURLObj.Fragment = ""
	fullURLWithoutFrag = fullURLObj.String()
	return
}
