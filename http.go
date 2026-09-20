package main

import "net/http"

const setInitConfBody = `{"result":{"sessionServerInfo":"XPN:/p=XTCP;ip=session.play.naver.jp;port=10123","staticDomain":"https://play-static.line-scdn.net/","nationCode":"JP","isGdprNation":false,"snsLoginUIList":["LD_GUEST"],"snsSignUpUIList":["LD_GUEST"]}}`

const notFoundBody = `{"errorCode":"404","errorMessage":"cherry: unknown route"}`

func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/v4/setInitConf", handleSetInitConf)
	mux.HandleFunc("/", handleNotFound)
	return mux
}

func handleSetInitConf(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, setInitConfBody)
}

func handleNotFound(w http.ResponseWriter, r *http.Request) {
	serveNotFound(w)
}

func serveNotFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, notFoundBody)
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
