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
	runCommand("java", "-jar",
		path.Join(closureCompilerDir, closureCompilerJar),
		"--entry_point=goog:historian.upload",
		"--js", "js/*.js",
		"--js", path.Join(closureLibraryDir, "closure/goog/base.js"),
		"--js", path.Join(closureLibraryDir, "closure/goog/**/*.js"),
		"--dependency_mode=PRUNE_LEGACY",
		"--generate_exports",
		"--js_output_file", path.Join(wd, compiledDir, "historian-optimized.js"),
		"--output_manifest", path.Join(wd, compiledDir, "manifest.MF"),
		"--compilation_level", "WHITESPACE_ONLY",
	)
}
