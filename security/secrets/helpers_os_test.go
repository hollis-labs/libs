package secrets_test

import "runtime"

func runtimeIsUnix() bool { return runtime.GOOS != "windows" }
func goosIsLinux() bool   { return runtime.GOOS == "linux" }
