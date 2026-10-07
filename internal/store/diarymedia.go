package store

import (
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

const DiaryDownloadPath = "/lineplay/d/download.nhn"

func ValidDiaryMediaTuple(userID, ctime, oid string) bool {
	if !ValidDiaryMediaUserID(userID) || !ValidDiaryMediaCTime(ctime) {
		return false
	}
	return oid == userID+"_"+ctime
}

func ValidDiaryMediaUserID(userID string) bool {
	if len(userID) == 0 || len(userID) > 128 {
		return false
	}
	for i := 0; i < len(userID); i++ {
		c := userID[i]
		if c < 0x21 || c > 0x7e || c == '/' || c == '\\' {
			return false
		}
	}
	return true
}

func ValidDiaryMediaCTime(ctime string) bool {
	if ctime == "" || len(ctime) > 19 {
		return false
	}
	for i := 0; i < len(ctime); i++ {
		if ctime[i] < '0' || ctime[i] > '9' {
			return false
		}
	}
	seconds, err := strconv.ParseInt(ctime, 10, 64)
	return err == nil && seconds >= 0 && strconv.FormatInt(seconds, 10) == ctime
}

func OneDiaryMediaQueryValue(query url.Values, key string) (string, bool) {
	values, ok := query[key]
	if ok && len(values) == 1 {
		return values[0], true
	}
	return "", false
}

func OptionalDiaryMediaQueryValue(query url.Values, key string) (string, bool) {
	values, ok := query[key]
	if !ok {
		return "", true
	}
	if len(values) != 1 {
		return "", false
	}
	if values[0] == "" {
		return "", false
	}
	return values[0], true
}

func ValidDiaryMediaVariant(path, tid string) bool {
	if path != DiaryDownloadPath {
		return false
	}
	return tid == "" || ValidDiaryMediaTID(tid)
}

func ValidDiaryMediaTID(tid string) bool {
	switch tid {
	case "306x0.r", "460x0.r", "612x0.r", "790x0.r", "320x480", "480x720", "640x960", "800x1200":
		return true
	default:
		return false
	}
}

func DiaryImageLocationType(images []DiaryImage) string {
	if len(images) == 0 {
		return "none"
	}
	for _, image := range images {
		if !ValidDiaryImage(image) {
			return "none"
		}
	}
	return "obs"
}

func NormalizeDiaryPost(post *DiaryPost) {
	if post.ImageLocationType == "" {
		post.ImageLocationType = DiaryImageLocationType(post.Images)
	}
}

func ValidDiaryImage(image DiaryImage) bool {
	if len(image.ImageURL) == 0 || len(image.ImageURL) > 512 || !utf8.ValidString(image.ImageURL) || strings.ContainsAny(image.ImageURL, "\\\r\n#") {
		return false
	}
	u, err := url.Parse(image.ImageURL)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Fragment != "" {
		return false
	}
	variantPath := u.Path
	if !strings.HasPrefix(variantPath, "/") {
		variantPath = "/" + variantPath
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query) < 3 || len(query) > 4 {
		return false
	}
	for key := range query {
		if key != "oid" && key != "ctime" && key != "userid" && key != "tid" {
			return false
		}
	}
	userID, userOK := OneDiaryMediaQueryValue(query, "userid")
	ctime, ctimeOK := OneDiaryMediaQueryValue(query, "ctime")
	oid, oidOK := OneDiaryMediaQueryValue(query, "oid")
	tid, tidOK := OptionalDiaryMediaQueryValue(query, "tid")
	if !userOK || !ctimeOK || !oidOK || !tidOK || !ValidDiaryMediaTuple(userID, ctime, oid) {
		return false
	}
	return ValidDiaryMediaVariant(variantPath, tid)
}
