package mvccview

import "errors"

// ErrViewClosed 表示对一个已经 Close 的 View 做了读取。
var ErrViewClosed = errors.New("mvccview: view closed")
