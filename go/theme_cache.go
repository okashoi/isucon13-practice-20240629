package main

import (
	"sync"
)

var (
	themeCache sync.Map
)

func AddThemeCache(userId int64, theme ThemeModel) {
	themeCache.Store(userId, theme)
}

func GetThemeCache(userId int64) (ThemeModel, bool) {
	theme, ok := themeCache.Load(userId)
	if !ok {
		return ThemeModel{}, false
	}
	return theme.(ThemeModel), true
}

func InitThemeCache() {
	themeCache = sync.Map{}
}
