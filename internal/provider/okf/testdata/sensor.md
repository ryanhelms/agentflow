---
type: Sensor Calibration
title: "O₂: bench #4"
description: >-
  Factory calibration
  for sensor 4.
tags: [sensors, "lab, west"]
x-calibration:
  sources:
    - instrument: &instrument "bench #4"
      alias: *instrument
      command: !recipe 'echo some text'
  template: '${SENSOR_TOKEN}'
executor:
  resource: ./tools/check.py
---
# Bench notes

---
type: This line belongs to the body.
