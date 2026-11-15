package sdcpp

import (
	"net/http"
	"regexp"
)

var cacheModes = map[string]bool{
	"easycache":  true,
	"ucache":     true,
	"dbcache":    true,
	"taylorseer": true,
	"cache-dit":  true,
	"spectrum":   true,
}

const cacheOptionItem = `[A-Za-z]+=[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)`

var cacheOptionPattern = regexp.MustCompile(`^` + cacheOptionItem + `(?:,` + cacheOptionItem + `)*$`)

const maxCacheOption = 200

func checkCache(mode, option string) error {
	if mode == "" {
		if option != "" {
			return apiErr(http.StatusBadRequest, "invalid_params", "cache_option needs cache_mode")
		}
		return nil
	}
	if !cacheModes[mode] {
		return apiErr(http.StatusBadRequest, "invalid_params", "cache_mode %q is not one of easycache, ucache, dbcache, taylorseer, cache-dit, spectrum", mode)
	}
	if option == "" {
		return nil
	}
	if len(option) > maxCacheOption || !cacheOptionPattern.MatchString(option) {
		return apiErr(http.StatusBadRequest, "invalid_params", "cache_option must look like key=number[,key=number…], got %q", option)
	}
	return nil
}
