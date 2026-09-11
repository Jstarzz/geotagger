package maxmindgeo

import "os"

func statFile(path string) (os.FileInfo, error) { return os.Stat(path) }
