package dev.droidjev.instrument;

import android.app.Instrumentation;
import android.app.UiAutomation;
import android.content.ClipData;
import android.content.ClipboardManager;
import android.content.Context;
import android.graphics.Rect;
import android.net.LocalServerSocket;
import android.net.LocalSocket;
import android.os.Bundle;
import android.text.TextUtils;
import android.util.Base64;
import android.view.accessibility.AccessibilityEvent;

import java.io.BufferedReader;
import java.io.IOException;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;

/**
 * The resident layout server. Spawned detached by the host as
 * `am instrument -w dev.droidjev.instrument/dev.droidjev.instrument.Runner`;
 * onStart never returns, so the instrumentation (and this process) stays
 * resident exactly like appium's blocking test method, until force-stopped.
 *
 * Transport is an abstract unix socket (localabstract:droidjev-instrument):
 * per-device, so two emulators never collide; the host reaches it through
 * `adb forward tcp:<port> localabstract:droidjev-instrument`. One connection
 * per request — a fresh connect/write/read/close cannot go stale when the
 * instrumentation is restarted under it.
 *
 * Line protocol (one request line per connection):
 *   PING            -> "OK 1\n"
 *   DUMP            -> "<byte-length>\n<byte-length bytes of hierarchy XML>"
 *   CLIP <base64>   -> "OK\n" | "ERR <reason>\n"
 * The socket only starts listening AFTER UiAutomation connects, so a
 * successful PING implies the accessibility connection is live.
 */
public final class Runner extends Instrumentation {
    private static final String SOCKET_NAME = "droidjev-instrument";
    private static final String VERSION = "1";

    /** Latest toast text; captured by the a11y listener, consumed by DUMP. */
    private volatile String toast = "";

    @Override
    public void onCreate(Bundle arguments) {
        super.onCreate(arguments);
        // A plain Instrumentation schedules onStart nowhere on its own — the
        // runner must call start() (AndroidJUnitRunner does this for appium).
        start();
    }

    @Override
    public void onStart() {
        super.onStart();
        final UiAutomation ua = getUiAutomation();
        // Without FLAG_RETRIEVE_INTERACTIVE_WINDOWS getWindows() is always
        // empty; the default service info does not include it.
        android.accessibilityservice.AccessibilityServiceInfo si = ua.getServiceInfo();
        if (si != null) {
            si.flags |= android.accessibilityservice.AccessibilityServiceInfo.FLAG_RETRIEVE_INTERACTIVE_WINDOWS;
            ua.setServiceInfo(si);
        }
        ua.setOnAccessibilityEventListener(new UiAutomation.OnAccessibilityEventListener() {
            @Override
            public void onAccessibilityEvent(AccessibilityEvent event) {
                if (event.getEventType() == AccessibilityEvent.TYPE_NOTIFICATION_STATE_CHANGED
                        && event.getText() != null && !event.getText().isEmpty()) {
                    toast = TextUtils.join(" ", event.getText());
                }
            }
        });
        // The full panel (1344x2992-style), not the app area: DisplayMetrics
        // here reports the bar-inset size, which would clip every bounds
        // attribute short. Deprecated but exact; parity with appium's numbers.
        android.graphics.Point size = new android.graphics.Point();
        ((android.view.WindowManager) getContext().getSystemService(Context.WINDOW_SERVICE))
                .getDefaultDisplay().getRealSize(size);
        final Rect display = new Rect(0, 0, size.x, size.y);

        LocalServerSocket server = null;
        while (server == null) {
            try {
                server = new LocalServerSocket(SOCKET_NAME); // abstract namespace
            } catch (IOException e) {
                android.util.Log.i("droidjev", "socket bind failed: " + e);
                sleepQuiet(500); // a predecessor may still hold the socket
            }
        }
        android.util.Log.i("droidjev", "socket bound: " + SOCKET_NAME);

        while (true) {
            try {
                handle(ua, server.accept(), display);
            } catch (IOException e) {
                // one failed client never takes the server down
            }
        }
    }

    private void handle(UiAutomation ua, LocalSocket client, Rect display) {
        try {
            BufferedReader in = new BufferedReader(
                    new InputStreamReader(client.getInputStream(), StandardCharsets.UTF_8), 8192);
            String line = in.readLine();
            OutputStream out = client.getOutputStream();
            if (line == null) {
                return;
            }
            if (line.equals("PING")) {
                reply(out, ("OK " + VERSION + "\n").getBytes(StandardCharsets.UTF_8));
            } else if (line.equals("DUMP")) {
                String toastNow = toast;
                toast = "";
                byte[] xml = XmlDump.windowsToXml(ua, toastNow, display)
                        .getBytes(StandardCharsets.UTF_8);
                reply(out, (xml.length + "\n").getBytes(StandardCharsets.US_ASCII));
                reply(out, xml);
            } else if (line.startsWith("CLIP ")) {
                String err = clip(line.substring(5));
                reply(out, ((err == null ? "OK" : "ERR " + err) + "\n")
                        .getBytes(StandardCharsets.UTF_8));
            } else {
                reply(out, "ERR unknown command\n".getBytes(StandardCharsets.UTF_8));
            }
        } catch (Throwable t) {
            // Mirror appium's SafeRequestHandler: a failing command answers
            // with an error instead of killing the server.
            try {
                OutputStream out = client.getOutputStream();
                reply(out, ("ERR " + t.getClass().getSimpleName() + "\n")
                        .getBytes(StandardCharsets.UTF_8));
            } catch (Throwable ignored) {
                // client is gone
            }
        } finally {
            try {
                client.close();
            } catch (IOException ignored) {
                // already closed
            }
        }
    }

    /**
     * Writes the clipboard directly from the instrumentation's own context:
     * AOSP's ClipboardService allows OP_WRITE_CLIPBOARD without focus (only
     * reads are IME/focus-gated), which is exactly how appium's set_clipboard
     * has always worked. Returns null on success, else a short reason.
     */
    private String clip(String base64) {
        final String text;
        try {
            text = new String(Base64.decode(base64, Base64.DEFAULT), StandardCharsets.UTF_8);
        } catch (IllegalArgumentException e) {
            return "bad base64";
        }
        try {
            ClipboardManager cm =
                    (ClipboardManager) getTargetContext().getSystemService(Context.CLIPBOARD_SERVICE);
            cm.setPrimaryClip(ClipData.newPlainText("droidjev", text));
        } catch (Exception e) {
            return e.getClass().getSimpleName();
        }
        return null;
    }

    private static void reply(OutputStream out, byte[] b) throws IOException {
        out.write(b);
        out.flush();
    }

    private static void sleepQuiet(long ms) {
        try {
            Thread.sleep(ms);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }
}
