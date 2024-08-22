package main

import (
	"bytes"
	"encoding/xml"
	"os"
	"strings"
)

type SitemapUrl struct {
	Loc        string `xml:"loc"`
	Lastmod    string `xml:"lastmod,omitempty"`
	ChangeFreq string `xml:"changefreq,omitempty"`
	Priority   string `xml:"priority,omitempty"`
}

type SitemapGenerator struct {
	XMLName  xml.Name     `xml:"urlset"`
	Xmlns    string       `xml:"xmlns,attr"`
	Urls     []SitemapUrl `xml:"url"`
	Type     string       `xml:"-"`
	FilePath string       `xml:"-"`
}

type SitemapIndexGenerator struct {
	XMLName  xml.Name     `xml:"sitemapindex"`
	Xmlns    string       `xml:"xmlns,attr"`
	Type     string       `xml:"-"`
	Sitemaps []SitemapUrl `xml:"sitemap"`
	FilePath string       `xml:"-"`
}

func NewSitemapGenerator(sitemapType string, filePath string, load bool) *SitemapGenerator {
	generator := &SitemapGenerator{
		Type:     sitemapType,
		FilePath: filePath,
		Xmlns:    "http://www.sitemaps.org/schemas/sitemap/0.9",
	}
	if load {
		data, err := os.ReadFile(filePath)
		if err == nil {
			_ = generator.Load(data)
		}
	}

	return generator
}

func (g *SitemapGenerator) Load(data []byte) error {
	if g.Type == "xml" {
		err := xml.Unmarshal(data, g)
		if err != nil {
			return err
		}
	} else {
		links := strings.Split(string(bytes.TrimSpace(data)), "\r\n")
		g.Urls = make([]SitemapUrl, 0, len(links))
		for i := range links {
			g.Urls = append(g.Urls, SitemapUrl{Loc: links[i]})
		}
	}

	return nil
}

func (g *SitemapGenerator) AddLoc(loc string, lastMod string) {
	g.Urls = append(g.Urls, SitemapUrl{
		Loc:     loc,
		Lastmod: lastMod,
		//ChangeFreq: "daily",
		//Priority:   "0.8",
	})
}

func (g *SitemapGenerator) Exists(link string) bool {
	for i := range g.Urls {
		if g.Urls[i].Loc == link {
			return true
		}
	}

	return false
}

func (g *SitemapGenerator) Save() error {
	if g.Type == "xml" {
		output, err := xml.MarshalIndent(g, "  ", "    ")
		if err == nil {
			f, err := os.OpenFile(g.FilePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.ModePerm)
			if err != nil {
				return err
			}
			_, err = f.WriteString(xml.Header)
			_, err = f.Write(output)
			if err1 := f.Close(); err1 != nil && err == nil {
				err = err1
			}
			return err
		}

		return err
	} else {
		var links = make([]string, 0, len(g.Urls))
		for i := range g.Urls {
			links = append(links, g.Urls[i].Loc)
		}
		err := os.WriteFile(g.FilePath, []byte(strings.Join(links, "\r\n")), os.ModePerm)

		return err
	}
}

func NewSitemapIndexGenerator(sitemapType string, filePath string, load bool) *SitemapIndexGenerator {
	generator := &SitemapIndexGenerator{
		Type:     sitemapType,
		Xmlns:    "http://www.sitemaps.org/schemas/sitemap/0.9",
		FilePath: filePath,
	}
	if load {
		buf, err := os.ReadFile(filePath)
		if err == nil {
			_ = generator.Load(buf)
		}
	}

	return generator
}

func (s *SitemapIndexGenerator) Load(data []byte) error {
	if s.Type == "xml" {
		err := xml.Unmarshal(data, s)
		if err != nil {
			return err
		}
	} else {
		links := strings.Split(string(bytes.TrimSpace(data)), "\r\n")
		s.Sitemaps = make([]SitemapUrl, 0, len(links))
		for i := range links {
			s.Sitemaps = append(s.Sitemaps, SitemapUrl{Loc: links[i]})
		}
	}

	return nil
}

func (s *SitemapIndexGenerator) AddIndex(loc string) {
	s.Sitemaps = append(s.Sitemaps, SitemapUrl{
		Loc: loc,
	})
}

func (s *SitemapIndexGenerator) Exists(link string) bool {
	for i := range s.Sitemaps {
		if s.Sitemaps[i].Loc == link {
			return true
		}
	}

	return false
}

func (s *SitemapIndexGenerator) Save() error {
	if s.Type == "xml" {
		output, err := xml.Marshal(s)
		if err == nil {
			f, err := os.OpenFile(s.FilePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.ModePerm)
			if err != nil {
				return err
			}
			_, err = f.WriteString(xml.Header)
			_, err = f.Write(output)
			if err1 := f.Close(); err1 != nil && err == nil {
				err = err1
			}
			return err
		}

		return err
	} else {
		var links = make([]string, 0, len(s.Sitemaps))
		for i := range s.Sitemaps {
			links = append(links, s.Sitemaps[i].Loc)
		}
		err := os.WriteFile(s.FilePath, []byte(strings.Join(links, "\r\n")), os.ModePerm)
		return err
	}
}
