package rawjson

import "github.com/stdotwork/pluxy/next/internal/jsonscan"

func Unmarshal(data []byte, v any) error { return jsonscan.Unmarshal(data, v) }
