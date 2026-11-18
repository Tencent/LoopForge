package spec

// helperSource is the JDK-only runtime emitted next to every compiled spec.
// It replaces AegisOS's AegisSupport for the subset LoopForge ports: HTTP call,
// status assertion and a small JSON-path assertion (dot fields and [index]).
const helperSource = `package aegis.generated;

import java.net.URI;
import java.net.URLEncoder;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

final class LoopForgeHttp {

  record Response(int status, String body) {}

  static Response call(String method, String path, Map<String, String> query, String body) throws Exception {
    String base = System.getProperty("aegis.baseUrl");
    if (base == null) {
      base = System.getenv().getOrDefault("LOOPFORGE_SUBJECT_URL", "http://127.0.0.1:8080");
    }
    StringBuilder url = new StringBuilder(base).append(path);
    if (!query.isEmpty()) {
      url.append(path.contains("?") ? '&' : '?');
      boolean first = true;
      for (Map.Entry<String, String> entry : query.entrySet()) {
        if (!first) {
          url.append('&');
        }
        first = false;
        url.append(URLEncoder.encode(entry.getKey(), StandardCharsets.UTF_8))
           .append('=')
           .append(URLEncoder.encode(entry.getValue(), StandardCharsets.UTF_8));
      }
    }
    HttpRequest.Builder builder = HttpRequest.newBuilder(URI.create(url.toString()))
        .timeout(Duration.ofSeconds(30))
        .header("Accept", "application/json");
    if (body == null || body.isEmpty()) {
      builder.method(method, HttpRequest.BodyPublishers.noBody());
    } else {
      builder.header("Content-Type", "application/json")
             .method(method, HttpRequest.BodyPublishers.ofString(body));
    }
    HttpResponse<String> response = HttpClient.newBuilder()
        .connectTimeout(Duration.ofSeconds(10))
        .build()
        .send(builder.build(), HttpResponse.BodyHandlers.ofString());
    return new Response(response.statusCode(), response.body());
  }

  static void assertJsonPath(String body, String path, String expected) {
    Object value = Json.parse(body).at(path);
    String actual = value == null ? "null" : String.valueOf(value);
    if (!expected.equals(actual)) {
      throw new AssertionError("json path " + path + " = " + actual + ", want " + expected);
    }
  }

  private static final class Json {
    private final Object root;

    private Json(Object root) {
      this.root = root;
    }

    static Json parse(String text) {
      Parser parser = new Parser(text);
      Object value = parser.value();
      parser.skip();
      if (!parser.done()) {
        throw new IllegalArgumentException("trailing JSON content");
      }
      return new Json(value);
    }

    Object at(String path) {
      Object current = root;
      int index = 0;
      while (index < path.length()) {
        char ch = path.charAt(index);
        if (ch == '.') {
          int end = index + 1;
          while (end < path.length() && path.charAt(end) != '.' && path.charAt(end) != '[') {
            end++;
          }
          current = member(current, path.substring(index + 1, end));
          index = end;
        } else if (ch == '[') {
          int end = path.indexOf(']', index);
          int position = Integer.parseInt(path.substring(index + 1, end));
          current = element(current, position);
          index = end + 1;
        } else {
          throw new IllegalArgumentException("bad json path " + path);
        }
      }
      return current;
    }

    @SuppressWarnings("unchecked")
    private static Object member(Object value, String name) {
      if (!(value instanceof Map)) {
        return null;
      }
      return ((Map<String, Object>) value).get(name);
    }

    @SuppressWarnings("unchecked")
    private static Object element(Object value, int position) {
      if (!(value instanceof List)) {
        return null;
      }
      List<Object> list = (List<Object>) value;
      return position < list.size() ? list.get(position) : null;
    }

    private static final class Parser {
      private final String text;
      private int index;

      Parser(String text) {
        this.text = text;
      }

      boolean done() {
        return index >= text.length();
      }

      void skip() {
        while (index < text.length() && Character.isWhitespace(text.charAt(index))) {
          index++;
        }
      }

      Object value() {
        skip();
        char ch = text.charAt(index);
        if (ch == '{') {
          return object();
        }
        if (ch == '[') {
          return array();
        }
        if (ch == '"') {
          return string();
        }
        if (text.startsWith("true", index)) {
          index += 4;
          return Boolean.TRUE;
        }
        if (text.startsWith("false", index)) {
          index += 5;
          return Boolean.FALSE;
        }
        if (text.startsWith("null", index)) {
          index += 4;
          return null;
        }
        return number();
      }

      private Map<String, Object> object() {
        Map<String, Object> map = new LinkedHashMap<>();
        index++;
        skip();
        if (text.charAt(index) == '}') {
          index++;
          return map;
        }
        while (true) {
          skip();
          String key = string();
          skip();
          index++; // :
          map.put(key, value());
          skip();
          char ch = text.charAt(index++);
          if (ch == '}') {
            return map;
          }
        }
      }

      private List<Object> array() {
        List<Object> list = new ArrayList<>();
        index++;
        skip();
        if (text.charAt(index) == ']') {
          index++;
          return list;
        }
        while (true) {
          list.add(value());
          skip();
          char ch = text.charAt(index++);
          if (ch == ']') {
            return list;
          }
        }
      }

      private String string() {
        if (text.charAt(index) != '"') {
          throw new IllegalArgumentException("expected string at " + index);
        }
        index++;
        StringBuilder out = new StringBuilder();
        while (true) {
          char ch = text.charAt(index++);
          if (ch == '"') {
            return out.toString();
          }
          if (ch == '\\') {
            char escape = text.charAt(index++);
            switch (escape) {
              case 'n' -> out.append('\n');
              case 'r' -> out.append('\r');
              case 't' -> out.append('\t');
              case 'b' -> out.append('\b');
              case 'f' -> out.append('\f');
              case 'u' -> {
                out.append((char) Integer.parseInt(text.substring(index, index + 4), 16));
                index += 4;
              }
              default -> out.append(escape);
            }
          } else {
            out.append(ch);
          }
        }
      }

      private Object number() {
        int start = index;
        while (index < text.length() && "-+.eE0123456789".indexOf(text.charAt(index)) >= 0) {
          index++;
        }
        String number = text.substring(start, index);
        if (number.contains(".") || number.contains("e") || number.contains("E")) {
          return Double.parseDouble(number);
        }
        return Long.parseLong(number);
      }
    }
  }
}
`
