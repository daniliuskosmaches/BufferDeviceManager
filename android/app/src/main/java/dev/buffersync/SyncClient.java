package dev.buffersync;

import java.io.ByteArrayOutputStream;
import java.io.InputStream;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.cert.CertificateException;
import java.security.cert.X509Certificate;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import javax.net.ssl.HttpsURLConnection;
import javax.net.ssl.SSLContext;
import javax.net.ssl.SSLSocketFactory;
import javax.net.ssl.TrustManager;
import javax.net.ssl.X509TrustManager;

/** HTTPS long polling: the server wakes GET on a change, not on a clipboard timer. */
final class SyncClient implements AutoCloseable {
    static final int MAX_BYTES = 65536;
    interface Listener {
        void connected();
        void received(String text);
        void sent();
        void failed(String message);
    }
    private final Pairing pairing;
    private final Listener listener;
    private final SSLSocketFactory ssl;
    private final Set<HttpsURLConnection> requests = ConcurrentHashMap.newKeySet();
    private final ExecutorService sender = Executors.newSingleThreadExecutor();
    private volatile boolean closed;
    private boolean sending;

    SyncClient(Pairing pairing, Listener listener) throws Exception {
        this.pairing = pairing;
        this.listener = listener;
        SSLContext context = SSLContext.getInstance("TLS");
        context.init(null, new TrustManager[]{new X509TrustManager() {
            public X509Certificate[] getAcceptedIssuers() { return new X509Certificate[0]; }
            public void checkClientTrusted(X509Certificate[] chain, String type) throws CertificateException {
                throw new CertificateException("client certificate not supported");
            }
            public void checkServerTrusted(X509Certificate[] chain, String type) throws CertificateException {
                try {
                    if (chain == null || chain.length == 0) throw new CertificateException();
                    chain[0].checkValidity();
                    byte[] hash = MessageDigest.getInstance("SHA-256").digest(chain[0].getEncoded());
                    StringBuilder hex = new StringBuilder();
                    for (byte b : hash) hex.append(String.format("%02x", b & 255));
                    if (!pairing.pin.equals(hex.toString())) throw new CertificateException("certificate pin mismatch");
                } catch (Exception e) { throw new CertificateException("Проверка сертификата не пройдена", e); }
            }
        }}, null);
        ssl = context.getSocketFactory();
    }

    private HttpsURLConnection request(String query, String method) throws Exception {
        HttpsURLConnection connection = (HttpsURLConnection)
                new URI("https", null, pairing.host, pairing.port, "/clipboard", query, null).toURL().openConnection();
        connection.setSSLSocketFactory(ssl);
        // The exact certificate is verified above; this local identity has no DNS name.
        connection.setHostnameVerifier((host, session) -> host.equals(pairing.host));
        connection.setInstanceFollowRedirects(false);
        connection.setConnectTimeout(5000);
        connection.setReadTimeout(32000);
        connection.setUseCaches(false);
        connection.setRequestMethod(method);
        connection.setRequestProperty("Authorization", "Bearer " + pairing.key);
        requests.add(connection);
        if (closed) { release(connection); throw new IllegalStateException("closed"); }
        return connection;
    }
    private void release(HttpsURLConnection connection) {
        requests.remove(connection);
        connection.disconnect();
    }
    void start() {
        new Thread(() -> {
            long revision = 0;
            boolean announced = false;
            while (!closed) {
                HttpsURLConnection connection = null;
                try {
                    connection = request(announced ? "after=" + revision : "check=1", "GET");
                    int code = connection.getResponseCode();
                    if (code != 200 && code != 204) throw new IllegalStateException("HTTP " + code);
                    if (!announced) { announced = true; if (!closed) listener.connected(); }
                    if (code == 200) {
                        long next = Long.parseLong(connection.getHeaderField("X-Revision"));
                        if (next <= revision) throw new IllegalStateException("bad revision");
                        String text = readText(connection.getInputStream());
                        revision = next;
                        if (!closed) listener.received(text);
                    }
                } catch (Exception e) {
                    if (!closed) listener.failed("Связь прервана. Проверьте Wi-Fi, программу на ПК и ссылку, затем подключитесь снова.");
                    close();
                } finally { if (connection != null) release(connection); }
            }
        }, "buffersync-receive").start();
    }
    synchronized boolean send(String text) {
        if (closed || sending) return false;
        sending = true;
        sender.execute(() -> {
            HttpsURLConnection connection = null;
            boolean success = false;
            try {
                byte[] data = text.getBytes(StandardCharsets.UTF_8);
                if (!valid(text)) throw new IllegalArgumentException("invalid text");
                connection = request(null, "POST");
                connection.setReadTimeout(10000);
                connection.setDoOutput(true);
                connection.setRequestProperty("Content-Type", "text/plain; charset=utf-8");
                connection.setFixedLengthStreamingMode(data.length);
                try (java.io.OutputStream out = connection.getOutputStream()) { out.write(data); }
                if (connection.getResponseCode() != 204) throw new IllegalStateException("send rejected");
                success = true;
            } catch (Exception e) {
                if (!closed) listener.failed("Отправка не подтверждена. Переподключитесь и повторите отправку.");
                close();
            } finally {
                if (connection != null) release(connection);
                synchronized (this) { sending = false; }
            }
            if (success && !closed) listener.sent();
        });
        return true;
    }
    static boolean valid(String text) {
        return text != null && !text.isEmpty() && text.indexOf('\0') < 0
                && !text.startsWith("buffersync://") && text.getBytes(StandardCharsets.UTF_8).length <= MAX_BYTES;
    }
    private static String readText(InputStream input) throws Exception {
        try (InputStream in = input; ByteArrayOutputStream out = new ByteArrayOutputStream()) {
            byte[] buffer = new byte[4096];
            int n;
            while ((n = in.read(buffer)) != -1) {
                if (out.size() + n > MAX_BYTES) throw new IllegalStateException("too much text");
                out.write(buffer, 0, n);
            }
            String text = out.toString("UTF-8");
            if (!valid(text)) throw new IllegalStateException("invalid text");
            return text;
        }
    }
    @Override public synchronized void close() {
        closed = true;
        if (sender.isShutdown()) return;
        sender.shutdownNow();
        // disconnect() can wait on network I/O: never block the Android UI thread.
        new Thread(() -> {
            for (HttpsURLConnection request : requests) request.disconnect();
            requests.clear();
        }, "buffersync-close").start();
    }
}
