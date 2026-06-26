package server

import (
	"net/url"
	"strconv"
)

// query-string helpers shared by the designed tools.

func addInts(q url.Values, key string, vals []int) {
	for _, v := range vals {
		q.Add(key, strconv.Itoa(v))
	}
}

func addInt(q url.Values, key string, v *int) {
	if v != nil {
		q.Set(key, strconv.Itoa(*v))
	}
}

func addBool(q url.Values, key string, v *bool) {
	if v != nil {
		q.Set(key, strconv.FormatBool(*v))
	}
}

func addStr(q url.Values, key, v string) {
	if v != "" {
		q.Set(key, v)
	}
}
