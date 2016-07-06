package main

import (
  "log"
)

func checkErr(err error) {
  if err != nil {
    log.Fatalf("error: %s", err)
  }
}
