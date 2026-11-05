package web

import (
	"net/url"
	"strconv"
)

// PageLink 是分页中的一个按钮。
type PageLink struct {
	Label    string
	URL      string
	Disabled bool
	Current  bool
	Ellipsis bool
}

// Pager 分页信息，模板 partials/pager.html 负责渲染。
type Pager struct {
	Page    int
	Pages   int
	Total   int
	PerPage int
	Links   []PageLink
}

// NewPager 生成分页（逻辑与 PHP 版 pagination_links 一致）。
func NewPager(page, total, perPage int, baseURL string, query url.Values) Pager {
	if perPage < 1 {
		perPage = 1
	}
	pages := (total + perPage - 1) / perPage
	if pages < 1 {
		pages = 1
	}
	p := Pager{Page: page, Pages: pages, Total: total, PerPage: perPage}
	if pages <= 1 {
		return p
	}
	make := func(target int, label string, disabled, current bool) PageLink {
		q := url.Values{}
		for k, v := range query {
			q[k] = v
		}
		q.Set("page", strconv.Itoa(target))
		return PageLink{Label: label, URL: baseURL + "?" + q.Encode(), Disabled: disabled, Current: current}
	}
	p.Links = append(p.Links, make(max(1, page-1), "上一页", page <= 1, false))
	start, end := max(1, page-2), min(pages, page+2)
	if start > 1 {
		p.Links = append(p.Links, make(1, "1", false, false))
		if start > 2 {
			p.Links = append(p.Links, PageLink{Label: "…", Ellipsis: true})
		}
	}
	for i := start; i <= end; i++ {
		p.Links = append(p.Links, make(i, strconv.Itoa(i), false, i == page))
	}
	if end < pages {
		if end < pages-1 {
			p.Links = append(p.Links, PageLink{Label: "…", Ellipsis: true})
		}
		p.Links = append(p.Links, make(pages, strconv.Itoa(pages), false, false))
	}
	p.Links = append(p.Links, make(min(pages, page+1), "下一页", page >= pages, false))
	return p
}
