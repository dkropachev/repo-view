# Third-party notices

## tree-sitter-c-sharp

`internal/csharpgrammar/language_generated.go` and
`internal/csharpgrammar/language_tables.bin` contain generated parse tables
derived from
[tree-sitter-c-sharp](https://github.com/tree-sitter/tree-sitter-c-sharp), and
`internal/csharpgrammar/scanner.go` is a pure-Go port of its external scanner.
The pinned source commit and checksums are recorded in
`internal/csharpgrammar/README.md`. The upstream software is licensed under the
MIT License reproduced in
`internal/csharpgrammar/LICENSE.tree-sitter-c-sharp`.

## tree-sitter-kotlin

`internal/kotlingrammar/language_generated.go` and
`internal/kotlingrammar/language_tables.bin` contain generated parse tables
derived from
[tree-sitter-kotlin](https://github.com/fwcd/tree-sitter-kotlin) commit
`1852ea17b7f60fb3f9d84e0b1555d56b46b39fb1`, and
`internal/kotlingrammar/scanner.go` is a pure-Go port of its external scanner.
The pinned source checksums are recorded in `internal/kotlingrammar/README.md`.
The upstream software is licensed under the MIT License reproduced in
`internal/kotlingrammar/LICENSE.tree-sitter-kotlin`.

## tree-sitter-swift

`internal/swiftgrammar/language_generated.go` and
`internal/swiftgrammar/language_tables.bin` contain generated parse tables
derived from
[tree-sitter-swift](https://github.com/alex-pinkus/tree-sitter-swift) commit
`8d02b7ff390a17a43ce90c4e987c49315cfc4be6`, and
`internal/swiftgrammar/scanner.go` is a bounded pure-Go port of its external
scanner. `internal/swiftgrammar/testdata/tree-sitter-swift-corpus` contains the
upstream grammar corpus used for exact-tree conformance tests. The pinned
source and generator checksums are recorded in
`internal/swiftgrammar/README.md`. The upstream software is licensed under the
MIT License reproduced in `internal/swiftgrammar/LICENSE.tree-sitter-swift`.

## Unicode Character Database 17.0.0

`repoview/java_lex.go` contains compact Java identifier-property ranges, and
`repoview/cpp_unicode17_names.go` contains compact C++ character-name data,
derived from the [Unicode Character Database 17.0.0](https://www.unicode.org/Public/17.0.0/ucd/),
copyright © 2025 Unicode, Inc. The data is licensed under the Unicode License
V3 reproduced below.

## unicode-ident

`repoview/rust_xid.go` contains generated identifier-property data derived
from [unicode-ident 1.0.24](https://github.com/dtolnay/unicode-ident), by David
Tolnay. The upstream generated data is licensed under the Unicode License V3
and, at the recipient's option, the MIT License reproduced below.

### MIT License

Permission is hereby granted, free of charge, to any person obtaining a copy of
this software and associated documentation files (the "Software"), to deal in
the Software without restriction, including without limitation the rights to
use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of
the Software, and to permit persons to whom the Software is furnished to do so,
subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

### Unicode License V3

COPYRIGHT AND PERMISSION NOTICE

Copyright © 1991-2026 Unicode, Inc.

NOTICE TO USER: Carefully read the following legal agreement. BY DOWNLOADING,
INSTALLING, COPYING OR OTHERWISE USING DATA FILES, AND/OR SOFTWARE, YOU
UNEQUIVOCALLY ACCEPT, AND AGREE TO BE BOUND BY, ALL OF THE TERMS AND CONDITIONS
OF THIS AGREEMENT. IF YOU DO NOT AGREE, DO NOT DOWNLOAD, INSTALL, COPY,
DISTRIBUTE OR USE THE DATA FILES OR SOFTWARE.

Permission is hereby granted, free of charge, to any person obtaining a copy of
data files and any associated documentation (the "Data Files") or software and
any associated documentation (the "Software") to deal in the Data Files or
Software without restriction, including without limitation the rights to use,
copy, modify, merge, publish, distribute, and/or sell copies of the Data Files
or Software, and to permit persons to whom the Data Files or Software are
furnished to do so, provided that either (a) this copyright and permission
notice appear with all copies of the Data Files or Software, or (b) this
copyright and permission notice appear in associated Documentation.

THE DATA FILES AND SOFTWARE ARE PROVIDED "AS IS", WITHOUT WARRANTY OF ANY
KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT OF THIRD
PARTY RIGHTS.

IN NO EVENT SHALL THE COPYRIGHT HOLDER OR HOLDERS INCLUDED IN THIS NOTICE BE
LIABLE FOR ANY CLAIM, OR ANY SPECIAL INDIRECT OR CONSEQUENTIAL DAMAGES, OR ANY
DAMAGES WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF OR IN
CONNECTION WITH THE USE OR PERFORMANCE OF THE DATA FILES OR SOFTWARE.

Except as contained in this notice, the name of a copyright holder shall not be
used in advertising or otherwise to promote the sale, use or other dealings in
these Data Files or Software without prior written authorization of the
copyright holder.
