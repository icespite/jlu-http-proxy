package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"html"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"regexp"
	"strings"
)

func (p *Proxy) HandleRequest(w http.ResponseWriter, r *http.Request) {
	var protocol = "https"
	var host = "vpns.jlu.edu.cn"
	var path = ""

	r.URL.Host = r.Host

	if r.URL.Host != "vpns.jlu.edu.cn" {
		if strings.Contains(r.URL.Path, "wengine-vpn") {
			// wdnmd
			w.WriteHeader(200)
			return
		}
	} else if strings.HasPrefix(r.URL.Path, "/jlu-http-proxy") {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		path = r.URL.Path[15:]
		switch path {
		case "/redirect":
			link, err := base64.StdEncoding.DecodeString(r.URL.RawQuery[4:])
			if err != nil {
				w.WriteHeader(403)
				return
			}

			w.Header().Set("Location", string(link))
			w.WriteHeader(302)
		}
		return
	}

	var toRequest string
	if PathMatchRegex.MatchString(r.URL.Path) {
		// Keep vpns
		if r.URL.Host == "vpns.jlu.edu.cn" {
			// Save information before encode
			path = r.URL.Path
		} else {
			ret := PathMatchRegex.FindStringSubmatch(r.URL.Path)
			protocol = ret[1]
			host = ret[2]
			path = ret[3]

			w.Header().Set("Location", protocol+"://"+Decrypt(host)+path)
			w.WriteHeader(302)
			return
			// r.URL.Host = "vpns.jlu.edu.cn"
			// r.URL.Path = "/" + protocol + "-" + r.URL.Port() + "/" + host + path
		}
	} else if r.URL.Host != "vpns.jlu.edu.cn" {
		protocol = r.URL.Scheme
		host = r.URL.Hostname()

		// Without VPN
		r.URL.Path = "/" + protocol + "-" + r.URL.Port() + "/" + Encrypt(host) + r.URL.Path
		r.URL.Scheme = protocol
		r.URL.Host = "vpns.jlu.edu.cn"
	}
	r.URL.Scheme = "https"

	// Construct new request
	toRequest = r.URL.String()
	req, err := http.NewRequest(r.Method, toRequest, r.Body)
	if err != nil {
		log.Println(err)
		return
	}

	// Headers
	req.Header = r.Header
	req.Header.Del("Proxy-Connection")
	req.Header.Set("Referer", toRequest)

	// Set headers
	cookies := req.Header.Get("Cookie")
	// if cookies != "" {
	//	_, err = p.SimpleFetch("POST", "/wengine-vpn/cookie?method=set&host="+host+"&scheme="+protocol+"&path="+path+"&ck_data="+url.QueryEscape(cookies), nil)
	//	if err != nil {
	//		log.Println(err)
	//	}
	// }
	if cookies == "" {
		cookies = p.Cookies
	} else {
		reg := regexp.MustCompile("wengine_vpn_ticket=[0-9a-f]+")
		if reg.MatchString(cookies) {
			cookies = reg.ReplaceAllString(cookies, p.Cookies)
		} else {
			cookies += "; " + p.Cookies
		}
	}
	req.Header.Set("Cookie", cookies)

	// Do request
	if proxy.Http2 {
		req.Proto = "HTTP/2"
	} else {
		req.Proto = "HTTP/1.1"
	}
	resp, err := DefaultClient.Do(req)
	if err != nil {
		log.Println(err)
		w.WriteHeader(403)
		_, _ = w.Write([]byte(err.Error()))
		return
	}

	// Handle redirect
	if resp.StatusCode == 301 || resp.StatusCode == 302 {
		location := resp.Header.Get("Location")
		if location == "/login" {
			// disable and replace vpn login redirect
			// reauth and 302 to self
			_ = p.Login()
			w.Header().Set("Location", "http://vpns.jlu.edu.cn/jlu-http-proxy/redirect?url="+base64.StdEncoding.EncodeToString([]byte(toRequest)))
			w.WriteHeader(302)
		} else {
			location = RedirectLink.ReplaceAllStringFunc(location, func(s string) string {
				ret := RedirectLink.FindStringSubmatch(s)
				return ret[1] + "://" + Decrypt(ret[2]) + ret[3]
			})
			w.Header().Set("Location", location)
			w.WriteHeader(resp.StatusCode)
		}
		return
	}

	for k, v := range resp.Header {
		if k == "Content-Length" {
			continue
		}
		for _, vv := range v {
			w.Header().Add(k, vv)
		}
	}

	if r.Header.Get("Origin") != "" {
		w.Header().Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
	} else {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	}

	w.Header().Set("Access-Control-Allow-Credentials", "true")

	if r.Header.Get("Access-Control-Request-Headers") != "" {
		w.Header().Set("Access-Control-Allow-Headers", r.Header.Get("Access-Control-Request-Headers"))
	}

	for _, c := range resp.Cookies() {
		w.Header().Add("Set-Cookie", c.Raw)
	}

	w.WriteHeader(resp.StatusCode)

	body := resp.Body
	// Gzip
	if resp.Header.Get("Content-Encoding") == "gzip" {
		body, err = gzip.NewReader(resp.Body)
		if err != nil {
			log.Println(err)
			return
		}
	}
	defer body.Close()

	result, err := ioutil.ReadAll(body)
	if err != nil && err != io.EOF {
		log.Println(err)
	}

	// Restore vpns toRequest to original
	result = VPNsLinkMatch.ReplaceAllFunc(result, func(bytes []byte) []byte {
		var postfix string
		ret := VPNsLinkMatch.FindSubmatch(bytes)
		if len(ret) == 4 && string(ret[3]) != "" {
			postfix = "/" + string(ret[3])
		}

		link := string(ret[1]) + "://" + Decrypt(string(ret[2])) + postfix
		return []byte(link)
	})

	// Fix URL Escape in links
	result = LinkUnescape.ReplaceAllFunc(result, func(bytes []byte) []byte {
		ret := LinkUnescape.FindSubmatch(bytes)
		return []byte(string(ret[1]) + "=\"" + html.UnescapeString(string(ret[2])) + "\"")
	})

	// Un VPN ify
	result = VPNEvalPrefix.ReplaceAll(result, []byte{})
	result = VPNEvalPostfix.ReplaceAll(result, []byte{})
	result = VPNRewritePrefix.ReplaceAll(result, []byte{})
	result = VPNRewritePostfix.ReplaceAll(result, []byte{})
	result = VPNInjectPrefix.ReplaceAll(result, []byte{})
	result = VPNInjectPostfix.ReplaceAll(result, []byte{})
	result = VPNParamRemoveFirst.ReplaceAll(result, []byte{})
	result = VPNParamRemoveOther.ReplaceAll(result, []byte{})

	// Remove added tags
	result = VPNScriptInfo.ReplaceAll(result, []byte{})
	result = VPNScriptWEngine.ReplaceAll(result, []byte(""))

	// Trim
	result = bytes.Trim(result, "\r\n")

	// Gzip
	if resp.Header.Get("Content-Encoding") == "gzip" {
		var buf bytes.Buffer
		wr := gzip.NewWriter(&buf)
		_, _ = wr.Write(result)
		_ = wr.Close()
		result = buf.Bytes()
	}

	if _, err = w.Write(result); err != nil {
		log.Println(err)
	}
}
