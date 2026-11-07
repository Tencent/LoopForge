# Apache ECharts 6.1.0

`echarts.min.js` is `dist/echarts.min.js` from the npm package `echarts@6.1.0`, unmodified.

- sha256: `b66b25aeb4df84e33199dc21694014d336d222cbd9deb0e5a7c14bd6aa0d0fd0`
- License: Apache-2.0 (`LICENSE`, `NOTICE`; bundled d3 code under `LICENSE-d3`)
- Loaded only by `web/app/obs-charts.js` (`obsEnsureECharts`) when the 统计 page opens; served same-origin so CSP `script-src 'self'` is unchanged.
- The full build is used because the official `echarts.common` build omits the heatmap series.

Upgrade: `npm pack echarts@<version>`, copy `package/dist/echarts.min.js`, `LICENSE`, `NOTICE`, `licenses/LICENSE-d3`, then update the version in this file, `OBS_ECHARTS_VERSION` in `web/app/obs-charts.js`, and the sha256 in `web/obs_charts_test.go`.
