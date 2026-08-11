# Third-party notices

## tree-sitter-c-sharp

`internal/csharpgrammar/language_generated.go` and
`internal/csharpgrammar/language_tables.bin` contain generated parse tables
derived from
[tree-sitter-c-sharp](https://github.com/tree-sitter/tree-sitter-c-sharp), and
`internal/csharpgrammar/scanner.go` is a pure-Go port of its external scanner.
The pinned source commit and checksums are recorded in
`internal/csharpgrammar/README.md`. The upstream software is licensed under the
MIT License reproduced below.

Copyright (c) 2014-2023 Max Brunsfeld, Damien Guard, Amaan Qureshi, and
contributors.

## tree-sitter-kotlin

`internal/kotlingrammar/language_generated.go` and
`internal/kotlingrammar/language_tables.bin` contain generated parse tables
derived from
[tree-sitter-kotlin](https://github.com/fwcd/tree-sitter-kotlin) commit
`1852ea17b7f60fb3f9d84e0b1555d56b46b39fb1`, and
`internal/kotlingrammar/scanner.go` is a pure-Go port of its external scanner.
The pinned source checksums are recorded in `internal/kotlingrammar/README.md`.
The upstream software is licensed under the MIT License reproduced below.

Copyright (c) 2019 fwcd

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
MIT License reproduced below.

Copyright (c) 2021 alex-pinkus

## treesitter-go

The compiled `scopesifter` binary incorporates
[`github.com/dcosson/treesitter-go`](https://github.com/dcosson/treesitter-go)
version `v0.1.0`. The upstream software is licensed under the MIT License
reproduced below.

Copyright (c) 2026 Danny Cosson

That module implements the runtime against tree-sitter `v0.26.6` and embeds
generated parse tables and scanner ports from the following grammar releases
used by `scopesifter`:

- [`tree-sitter-c`](https://github.com/tree-sitter/tree-sitter-c) `v0.24.1`;
- [`tree-sitter-cpp`](https://github.com/tree-sitter/tree-sitter-cpp) `v0.23.4`;
- [`tree-sitter-java`](https://github.com/tree-sitter/tree-sitter-java) `v0.23.5`;
- [`tree-sitter-javascript`](https://github.com/tree-sitter/tree-sitter-javascript)
  `v0.25.0`;
- [`tree-sitter-typescript`](https://github.com/tree-sitter/tree-sitter-typescript)
  `v0.23.2`, including its TypeScript and TSX grammars;
- [`tree-sitter-python`](https://github.com/tree-sitter/tree-sitter-python)
  `v0.23.6`; and
- [`tree-sitter-rust`](https://github.com/tree-sitter/tree-sitter-rust) `v0.24.0`.

Those upstream components are also licensed under the MIT License reproduced
below, with these retained notices:

Copyright (c) 2018 Max Brunsfeld (tree-sitter runtime)

Copyright (c) 2014 Max Brunsfeld (C, C++, and JavaScript grammars)

Copyright (c) 2017 Ayman Nadeem (Java grammar)

Copyright (c) 2017 Max Brunsfeld (TypeScript and TSX grammars)

Copyright (c) 2016 Max Brunsfeld (Python grammar)

Copyright (c) 2017 Maxim Sokolov (Rust grammar)

## golang.org/x/text

The compiled `scopesifter` binary incorporates
[`golang.org/x/text`](https://pkg.go.dev/golang.org/x/text) version `v0.37.0`.
The upstream software is licensed under the BSD 3-Clause License reproduced
below and includes the additional patent grant reproduced below.

Copyright 2009 The Go Authors.

## Unicode Character Database 17.0.0

`navigator/java_lex.go` and `navigator/javascript_unicode.go` contain compact
identifier-property ranges, and `navigator/cpp_unicode17_names.go` contains
compact C++ character-name data, derived from the
[Unicode Character Database 17.0.0](https://www.unicode.org/Public/17.0.0/ucd/),
copyright © 2025 Unicode, Inc. `navigator/python_xid.go` contains identifier
property ranges derived through CPython 3.14.4's Unicode 16.0.0 identifier
predicates. The Unicode data is licensed under the Unicode License V3
reproduced below.

## unicode-ident

`navigator/rust_xid.go` contains generated identifier-property data derived
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

### BSD 3-Clause License

Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

   * Redistributions of source code must retain the above copyright notice,
this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above copyright
notice, this list of conditions and the following disclaimer in the
documentation and/or other materials provided with the distribution.
   * Neither the name of Google LLC nor the names of its contributors may be
used to endorse or promote products derived from this software without
specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT OWNER OR CONTRIBUTORS BE LIABLE FOR
ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES
(INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES;
LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON
ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS
SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

### Additional IP Rights Grant (Patents)

"This implementation" means the copyrightable works distributed by Google as
part of the Go project.

Google hereby grants to You a perpetual, worldwide, non-exclusive, no-charge,
royalty-free, irrevocable (except as stated in this section) patent license to
make, have made, use, offer to sell, sell, import, transfer and otherwise run,
modify and propagate the contents of this implementation of Go, where such
license applies only to those patent claims, both currently owned or controlled
by Google and acquired in the future, licensable by Google that are necessarily
infringed by this implementation. This grant does not include claims that would
be infringed only as a consequence of further modification of this
implementation. If you or your agent or exclusive licensee institute or order
or agree to the institution of patent litigation against any entity (including
a cross-claim or counterclaim in a lawsuit) alleging that this implementation
of Go or any code incorporated within this implementation of Go constitutes
direct or contributory patent infringement, or inducement of patent
infringement, then any patent rights granted to you under this License for this
implementation of Go shall terminate as of the date such litigation is filed.

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
