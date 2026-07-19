// Copyright 2016 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Setup downloads needed Closure files and generates optimized JS files.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/reedhoop/ai-battery-historian/historianutils"
)

const (
	// 升级到 v20230802：旧版 v20170409 不支持 closure-library 中的 ES8+ 语法（async function），
	// 导致编译报 19 个 ERROR。新版要求 Java 11+。
	closureCompilerVersion = "20230802"
	closureCompilerJar     = "closure-compiler-v" + closureCompilerVersion + ".jar"
	// 新版 closure-compiler 发布在 Maven Central，不再发布到 dl.google.com 的 zip 包。
	closureCompilerURL = "https://repo1.maven.org/maven2/com/google/javascript/closure-compiler/v" + closureCompilerVersion + "/" + closureCompilerJar

	thirdPartyDir = "third_party"
	compiledDir   = "compiled"
)

var rebuild = flag.Bool("rebuild", false, "Whether or not clear all setup files and start from scratch.")

// runCommand runs the given command and only prints the output or error if they're not empty.
func runCommand(name string, args ...string) {
	out, err := historianutils.RunCommand(name, args...)
	if err != nil {
		fmt.Println(err)
	}
	if out != "" {
		fmt.Println(out)
	}
}

// saveFile saves the given contents to the path. relPath must point directly to the file to write to.
func saveFile(relPath string, contents []byte) error {
	f, err := os.Create(relPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, bytes.NewReader(contents))
	return err
}

func deletePath(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// Path doesn't exist. Nothing to delete.
		return nil
	}
	if runtime.GOOS == "windows" {
		// os.RemoveAll won't remove read-only files (eg. .git files) on Windows.
		// Modify the permissions path to be writable on Windows.
		filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			return os.Chmod(p, 0777)
		})
	}
	return os.RemoveAll(path)
}

func main() {
	flag.Parse()

	if *rebuild {
		fmt.Println("\nClearing files...")
		if err := deletePath(thirdPartyDir); err != nil {
			fmt.Printf("Failed to delete %s directory: %v\n", thirdPartyDir, err)
			return
		}
		if err := deletePath(compiledDir); err != nil {
			fmt.Printf("Failed to delete %s directory: %v\n", compiledDir, err)
			return
		}
	}

	os.Mkdir(thirdPartyDir, 0777)
	os.Mkdir(compiledDir, 0777)

	wd, err := os.Getwd()
	if err != nil {
		fmt.Printf("Unable to get working directory: %v\n", err)
		return
	}
	closureLibraryDir := path.Join(wd, thirdPartyDir, "closure-library")
	closureCompilerDir := path.Join(wd, thirdPartyDir, "closure-compiler")
	axisDir := path.Join(thirdPartyDir, "flot-axislabels")

	if _, err := os.Stat(closureLibraryDir); os.IsNotExist(err) {
		fmt.Println("\nDownloading Closure library...")
		runCommand("git", "clone", "https://github.com/google/closure-library", closureLibraryDir)
	}

	_, errD := os.Stat(closureCompilerDir)
	_, errF := os.Stat(path.Join(closureCompilerDir, closureCompilerJar))
	if os.IsNotExist(errD) || os.IsNotExist(errF) {
		fmt.Println("\nDownloading Closure compiler...")
		// Current compiler, if any, is not current. Remove old files.
		if err := deletePath(closureCompilerDir); err != nil {
			fmt.Printf("Failed to clear compiler directory: %v\n", err)
		}
		// Download desired file.
		os.Mkdir(closureCompilerDir, 0777)

		resp, err := http.Get(closureCompilerURL)
		if err != nil {
			fmt.Printf("Failed to download Closure compiler: %v\n", err)
			fmt.Printf("\nIf this persists, please manually download the compiler jar from %s into the %s directory and rerun this script.\n\n", closureCompilerURL, closureCompilerDir)
			return
		}
		defer resp.Body.Close()

		contents, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			fmt.Printf("Couldn't get jar contents: %v\n", err)
			return
		}

		if err := saveFile(path.Join(closureCompilerDir, closureCompilerJar), contents); err != nil {
			fmt.Printf("Couldn't save Closure jar file: %v\n", err)
			return
		}
	}

	if _, err := os.Stat(axisDir); os.IsNotExist(err) {
		fmt.Println("\nDownloading 3rd-party JS files...")
		runCommand("git", "clone", "https://github.com/markrcote/flot-axislabels.git", axisDir)
	}

	fmt.Println("\nGenerating JS runfiles...")
	out, err := historianutils.RunCommand("python",
		path.Join(closureLibraryDir, "closure/bin/build/depswriter.py"),
		fmt.Sprintf(`--root=%s`, path.Join(closureLibraryDir, "closure", "goog")),
		`--root_with_prefix=js ../../../../js`)
	if err != nil {
		fmt.Printf("Couldn't generate runfile: %v\n", err)
		return
	}
	if err = saveFile(path.Join(wd, compiledDir, "historian_deps-runfiles.js"), []byte(out)); err != nil {
		fmt.Printf("Couldn't save runfiles file: %v\n", err)
		return
	}

	fmt.Println("\nGenerating optimized JS runfiles...")
	// 新版 closure-compiler v20230802 选项变更：
	//   --closure_entry_point → --entry_point=goog:<namespace>
	//   --only_closure_dependencies → --dependency_mode=PRUNE_LEGACY
	// 使用 WHITESPACE_ONLY 而非 SIMPLE_OPTIMIZATIONS：v20230802 在 PRUNE_LEGACY + SIMPLE_OPTIMIZATIONS
	// 模式下会把 historian.BarData.Legend={} 排序到 historian.BarData=function 之前，导致
	// "Cannot set properties of undefined (setting 'Legend')" 运行时错误。WHITESPACE_ONLY 保留
	// goog.provide 运行时调用，由 base.js 正确创建命名空间，避免排序问题。

	// 收集 closure/goog 下的 JS 输入，排除 demos/ 目录以及 *_test.js 文件，
	// 避免把 goog.testing/csp_test.js 等测试基础设施打进生产包。csp_test.js 的顶层代码会
	// 无条件注入 strict-dynamic 的 CSP，挡掉页面内联脚本，并让 closure 测试框架在首个测试前
	// 就检测到 CSP 违规，导致 csp_test 用例全部 setUpPage 失败。
	// 注意：不要整目录排除 testing/，否则会连 goog.testing.TestCase（测试运行器框架，被生产包
	// 间接依赖）一起剔除，引发 "namespace not provided" 编译错误。只排除 *_test.js 即可精准拿掉
	// csp_test.js，同时保留 testcase / cspviolationobserver 等运行所需文件。
	closureGoogDir := path.Join(closureLibraryDir, "closure", "goog")
	var closureJsArgs []string
	filepath.Walk(closureGoogDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if filepath.Ext(p) != ".js" {
			return nil
		}
		rel, _ := filepath.Rel(closureGoogDir, p)
		for _, part := range strings.Split(rel, string(os.PathSeparator)) {
			if part == "demos" {
				return nil
			}
		}
		if strings.HasSuffix(info.Name(), "_test.js") {
			return nil
		}
		closureJsArgs = append(closureJsArgs, "--js", p)
		return nil
	})

	// 应用的 JS 同样排除 *_test.js（测试文件不应进入生产包）。
	jsFiles, _ := filepath.Glob("js/*.js")
	var jsJsArgs []string
	for _, f := range jsFiles {
		if strings.HasSuffix(f, "_test.js") {
			continue
		}
		jsJsArgs = append(jsJsArgs, "--js", f)
	}

	compilerArgs := []string{
		"-jar",
		path.Join(closureCompilerDir, closureCompilerJar),
		"--entry_point=goog:historian.upload",
	}
	compilerArgs = append(compilerArgs, jsJsArgs...)
	compilerArgs = append(compilerArgs, closureJsArgs...)
	compilerArgs = append(compilerArgs,
		"--dependency_mode=PRUNE_LEGACY",
		"--generate_exports",
		"--js_output_file", path.Join(wd, compiledDir, "historian-optimized.js"),
		"--output_manifest", path.Join(wd, compiledDir, "manifest.MF"),
		"--compilation_level", "WHITESPACE_ONLY",
	)

	// Windows 对单条命令行的长度有上限（约 32K），当 --js 输入文件非常多时极易触发
	// "The filename or extension is too long"（fork/exec 失败），导致 setup 无法重建 bundle。
	// 改用 Java 的 @argfile 机制：把所有参数写入临时文件，再让 java 从文件读取，绕开命令行长度限制。
	argFileContent := strings.Builder{}
	for _, a := range compilerArgs {
		// Java @argfile 中反斜杠是转义符，统一转成前向斜杠避免解析错误（Windows 路径也接受 /）。
		a = strings.ReplaceAll(a, "\\", "/")
		argFileContent.WriteString(a)
		argFileContent.WriteString("\n")
	}
	argFile, err := ioutil.TempFile("", "closure-args-*.txt")
	if err != nil {
		fmt.Printf("Couldn't create closure argfile: %v\n", err)
		return
	}
	defer os.Remove(argFile.Name())
	if _, err := argFile.WriteString(argFileContent.String()); err != nil {
		fmt.Printf("Couldn't write closure argfile: %v\n", err)
		return
	}
	if err := argFile.Close(); err != nil {
		fmt.Printf("Couldn't close closure argfile: %v\n", err)
		return
	}
	// @ 之后的文件路径同样做 / 转换，规避 Java 对反斜杠转义的误处理。
	runCommand("java", "@"+strings.ReplaceAll(argFile.Name(), "\\", "/"))
}
