# zipreport-server

HTML to PDF conversion Webservice, written in Go.

### Command line options

| Option | Mandatory    | Description   |
| ---   | --- | --- |
| -addr=\<address\>| No | Address to listen (default *) |
| -port=\<port\> | No | Port to listen (default 6543)| 
| -keyfile=\<path\> | No | SSL certificate key file |
| -crtfile=\<path\> | No | SSL certificate file |
| -apikey=\<key\> | No | API key for authentication (via X-Auth-Key)|
| -storage=\<path\> | Yes | Path to temporary storage |
| -cli=\<path\>| Yes | Path to zipreport-cli |
| -httprt=\<seconds\>| No | Http server read timeout, in seconds (default 300)|
| -httpwt=\<seconds\>| No | Http server write timeout, in seconds (default 300)|
| -debug | No | Enable verbose output |
| -nometrics | No | Disable Prometheus metric endpoint|
| -no-sandbox| No| Disable Chromium sandbox in zipreport-cli|
| -version| No | Show current zipreport-server version|

### Available endpoints

#### [POST] /v1/render

**Format:** multipart/form-data

**Fields:**

|Field | Mandatory | Description |
|--- |--- | --- |
| report | Yes | Report file |
| page_size| Yes | Page size (A5/A4/A3/Letter/Legal/Tabloid)|
| margins|Yes | Margin type (none/minimal/standard) |
| landscape| No | If true, print in landscape|
| script | No | Main html file (default report.html) |
| settling_time | No | Settling time, in ms (default 200ms, see below) |
| timeout_render | No | Render timeout, in seconds (default 60s, see below)| 
| timeout_js | No | JS Event waiting time, in seconds (default 8s, see below)|
| timeout_process| No | CLI waiting time, in seconds (default 120s, see below)|
| js_event| No | If true, wait for the javascript event (see below) |
| ignore_ssl_errors| No | If true, ssl errors in referenced resources will be ignored|
| secure_only | No | If true, only secure content is allowed |

#

**settling_time** (default: 200)

Value in ms to wait after the DOM is ready to print the report. This setting is ignored if
js_event is enabled.


**timeout_render** (default 60)

Waiting time in seconds to perform the print operation, including waiting times such as 
settling_time and timeout_js.


**timeout_js** (default 8)

Time to wait, in seconds, for the javascript event to be triggered, before generating the
report anyway. Requires js_event to be true.


**timeout_process** (default 120)

Time to wait, in seconds, for the cli execution. After the value has passed, the process
is killed and the report generation is aborted.


**js_event**

If true, the system will wait upto timeout_js for a javascript event to be triggered before
generating the PDF. This allows for dynamic pages to signal when DOM manipulation is finished.

Triggering example: 
```javascript
<script>
(function() {
    // signal end of composition
    document.dispatchEvent(new Event('zpt-view-ready'))
})();
</script>
```
#### [GET] /metrics

Prometheus metrics endpoint. Besides the default internal GO metrics, the following are provided:

**total_request_success** (counter)

Total successful conversions

**total_request_error** (counter)

Total failed conversions

**conversion_time** (histogram)

Conversion time histogram, in seconds. The upper bound is 120.



### Authentication

If -apikey is specified, zipreport-server will perform header-based authentication with
the designated key. Clients should pass the key in the "X-Auth-Key" header.


### Build

To build the binary in ./bin as well as a self-signed certificate in ./cert, just run
make:

```shell script
$ make all
$ make certificate
```
