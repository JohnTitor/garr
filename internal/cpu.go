// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package internal

// CacheLinePad is the CPU's assumed cache line padding.
type CacheLinePad = [CacheLinePadSize]byte
