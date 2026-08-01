package hardware

import (
    "os/exec"
    "strconv"
    "strings"
)

var readSysctlIntFn = readSysctlInt

func readSwapPageCounts() (pageIn, pageOut uint64, err error) {
    pageIn, err = readSysctlIntFn("vm.pageins")
    if err != nil {
        return 0, 0, err
    }

    pageOut, err = readSysctlIntFn("vm.pageouts")
    if err != nil {
        return 0, 0, err
    }

    return pageIn, pageOut, nil
}

func readSysctlInt(name string) (uint64, error) {
    out, err := exec.Command("sysctl", "-n", name).Output()
    if err != nil {
        return 0, err
    }
    val := strings.TrimSpace(string(out))
    return strconv.ParseUint(val, 10, 64)
}
