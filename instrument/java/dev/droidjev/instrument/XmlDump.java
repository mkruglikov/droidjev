package dev.droidjev.instrument;

import android.app.UiAutomation;
import android.graphics.Rect;
import android.os.Build;
import android.view.accessibility.AccessibilityNodeInfo;
import android.view.accessibility.AccessibilityWindowInfo;

import java.util.List;

/**
 * Serializes the current window hierarchy to the XML dialect droidjev's parser
 * (layout.go) consumes, mirroring appium-uiautomator2-server's /source output
 * where the parser can tell the difference — and only there, by design:
 *
 *  - ALL windows with roots are emitted (never only getRootInActiveWindow):
 *    an open IME overlay's keys must appear in the dump. The ACTIVE window is
 *    emitted first — the first root's package is what droidjev reports as the
 *    focused app. Multiple roots are siblings under <hierarchy>, like appium.
 *  - Invisible CHILDREN are skipped (appium's default allowInvisibleElements=
 *    false), but window roots never are.
 *  - text for a RangeInfo node (sliders) is the current value as a float
 *    string, matching appium's AxNodeInfoHelper.getText.
 *  - bounds are appium's default calculation: node rect intersected with the
 *    display, the window, and the nearest scrollable ancestor. Scroll-clip is
 *    why a slider under the status bar reports a shorter track.
 *  - Depth is capped at 70 (appium's SnapshotMaxDepth default; the hierarchy
 *    root is depth 0, window roots depth 1).
 *  - A captured toast arrives as one extra root-level node (appium injects
 *    android.widget.Toast nodes the same way, displayed=true).
 *
 * Attributes are exactly the parser's vocabulary — extras the parser ignores
 * are not emitted. Attribute order is irrelevant to it.
 */
final class XmlDump {
    /** Appium's SnapshotMaxDepth default; window roots sit at depth 1. */
    private static final int MAX_DEPTH = 70;

    private XmlDump() {}

    static String windowsToXml(UiAutomation ua, String toast, Rect display) {
        StringBuilder sb = new StringBuilder(48 * 1024);
        sb.append("<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>\n");
        sb.append("<hierarchy rotation=\"0\">\n");
        int roots = 0;
        List<AccessibilityWindowInfo> windows = ua.getWindows();
        if (windows != null) {
            AccessibilityWindowInfo active = null;
            for (AccessibilityWindowInfo w : windows) {
                if (w.isActive() && w.getRoot() != null) {
                    active = w;
                    break;
                }
            }
            roots += appendWindows(sb, windows, active, display, 0);
            if (active != null) {
                roots += appendWindows(sb, windows, null, display, roots);
            }
        }
        if (toast != null && !toast.isEmpty()) {
            toastNode(sb, toast, roots);
        }
        sb.append("</hierarchy>");
        return sb.toString();
    }

    /**
     * One pass over the windows: only the given window when {@code only} is
     * non-null (the active window comes first), every root-bearing window
     * except it when null. Returns how many roots were appended.
     */
    private static int appendWindows(StringBuilder sb, List<AccessibilityWindowInfo> windows,
                                     AccessibilityWindowInfo only, Rect display, int startIdx) {
        int n = startIdx;
        for (AccessibilityWindowInfo w : windows) {
            if (w != only && (only != null || w.isActive())) {
                continue;
            }
            AccessibilityNodeInfo root = w.getRoot();
            if (root == null) {
                continue;
            }
            Rect win = new Rect();
            w.getBoundsInScreen(win);
            node(sb, root, n, 1, display, win, null);
            n++;
        }
        return n;
    }

    private static void node(StringBuilder sb, AccessibilityNodeInfo n, int index, int depth,
                             Rect display, Rect window, Rect scrollClip) {
        Rect r = new Rect();
        n.getBoundsInScreen(r);
        r.intersect(display);
        r.intersect(window);
        if (scrollClip != null) {
            r.intersect(scrollClip);
        }
        String cls = charSequence(n.getClassName());
        String tag = cls.isEmpty() ? "android.view.View" : cls;
        sb.append("  <").append(tag)
                .append(" index=\"").append(index).append('"')
                .append(" text=\"").append(esc(text(n))).append('"')
                .append(" resource-id=\"").append(esc(charSequence(n.getViewIdResourceName()))).append('"')
                .append(" class=\"").append(esc(cls)).append('"')
                .append(" package=\"").append(esc(charSequence(n.getPackageName()))).append('"')
                .append(" content-desc=\"").append(esc(charSequence(n.getContentDescription()))).append('"')
                .append(" checkable=\"").append(n.isCheckable()).append('"')
                .append(" checked=\"").append(n.isChecked()).append('"')
                .append(" clickable=\"").append(n.isClickable()).append('"')
                .append(" focusable=\"").append(n.isFocusable()).append('"')
                .append(" focused=\"").append(n.isFocused()).append('"')
                .append(" long-clickable=\"").append(n.isLongClickable()).append('"')
                .append(" password=\"").append(n.isPassword()).append('"')
                .append(" scrollable=\"").append(n.isScrollable()).append('"')
                .append(" selected=\"").append(n.isSelected()).append('"')
                .append(" bounds=\"[").append(r.left).append(',').append(r.top)
                .append("][").append(r.right).append(',').append(r.bottom).append("]\"")
                .append(" displayed=\"").append(n.isVisibleToUser()).append('"');
        if (n.getChildCount() == 0 || depth >= MAX_DEPTH) {
            sb.append("/>\n");
            return;
        }
        sb.append(">\n");
        Rect childClip = n.isScrollable() ? r : scrollClip;
        for (int i = 0; i < n.getChildCount(); i++) {
            AccessibilityNodeInfo c = n.getChild(i);
            if (c == null) {
                continue; // the tree changed mid-walk
            }
            if (c.isVisibleToUser()) {
                node(sb, c, i, depth + 1, display, window, childClip);
            }
            recycle(c);
        }
        sb.append("  </").append(tag).append(">\n");
    }

    private static void toastNode(StringBuilder sb, String text, int index) {
        sb.append("  <android.widget.Toast")
                .append(" index=\"").append(index).append('"')
                .append(" text=\"").append(esc(text)).append('"')
                .append(" class=\"android.widget.Toast\"")
                .append(" package=\"com.android.settings\"")
                .append(" checkable=\"false\" checked=\"false\" clickable=\"false\"")
                .append(" focusable=\"false\" focused=\"false\" long-clickable=\"false\"")
                .append(" password=\"false\" scrollable=\"false\" selected=\"false\"")
                .append(" bounds=\"[0,0][1,1]\"")
                .append(" displayed=\"true\"/>\n");
    }

    /** Sliders report their value as text, like appium's getText. */
    private static String text(AccessibilityNodeInfo n) {
        AccessibilityNodeInfo.RangeInfo range = n.getRangeInfo();
        if (range != null) {
            return Float.toString(range.getCurrent());
        }
        return charSequence(n.getText());
    }

    private static String charSequence(CharSequence cs) {
        return cs == null ? "" : cs.toString();
    }

    /**
     * Escape order is the reverse of the parser's decode order: &amp; is
     * decoded LAST there, so & is escaped FIRST here — `&amp;lt;` in text must
     * survive as the literal string "&lt;", not double-decode to "<".
     */
    private static String esc(String s) {
        if (s.indexOf('&') < 0 && s.indexOf('<') < 0 && s.indexOf('>') < 0
                && s.indexOf('"') < 0 && s.indexOf('\'') < 0) {
            return s;
        }
        StringBuilder sb = new StringBuilder(s.length() + 16);
        for (int i = 0; i < s.length(); i++) {
            char ch = s.charAt(i);
            if (ch == '&') {
                sb.append("&amp;");
            } else if (ch == '<') {
                sb.append("&lt;");
            } else if (ch == '>') {
                sb.append("&gt;");
            } else if (ch == '"') {
                sb.append("&quot;");
            } else if (ch == '\'') {
                sb.append("&apos;");
            } else {
                sb.append(ch);
            }
        }
        return sb.toString();
    }

    /** recycle() is a deprecated no-op on API 33+; harmless there either way. */
    private static void recycle(AccessibilityNodeInfo n) {
        if (Build.VERSION.SDK_INT < 33) {
            try {
                n.recycle();
            } catch (IllegalStateException ignored) {
                // already recycled
            }
        }
    }
}
