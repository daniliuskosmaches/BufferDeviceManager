package dev.buffersync;

import android.app.Activity;
import android.content.ClipData;
import android.content.ClipboardManager;
import android.content.Intent;
import android.graphics.Color;
import android.os.Build;
import android.os.Bundle;
import android.text.InputFilter;
import android.text.InputType;
import android.view.View;
import android.view.WindowInsets;
import android.widget.Button;
import android.widget.CheckBox;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;
import java.util.ArrayList;

/** One screen, no background service, accessibility tricks or clipboard permissions. */
public final class MainActivity extends Activity {
    private ClipboardManager clipboard;
    private EditText link, editor;
    private TextView connectionStatus, message;
    private Button connect;
    private CheckBox automatic, copyReceived;
    private LinearLayout history;
    private final ArrayList<String> items = new ArrayList<>();
    private SyncClient client;
    private boolean resumed, connected;
    private String lastObserved = "", pending = null, pendingOutgoing = null;
    private final ClipboardManager.OnPrimaryClipChangedListener clipboardListener = this::inspectClipboard;

    @Override public void onCreate(Bundle state) {
        super.onCreate(state);
        clipboard = (ClipboardManager) getSystemService(CLIPBOARD_SERVICE);
        ScrollView scroll = new ScrollView(this);
        scroll.setFillViewport(true);
        scroll.setBackgroundColor(Color.rgb(245,247,251));
        LinearLayout content = new LinearLayout(this);
        content.setOrientation(LinearLayout.VERTICAL);
        content.setPadding(dp(22), dp(18), dp(22), dp(26));
        scroll.addView(content);
        scroll.setOnApplyWindowInsetsListener((view, insets) -> {
            if (Build.VERSION.SDK_INT >= 30) {
                android.graphics.Insets bars = insets.getInsets(WindowInsets.Type.systemBars());
                view.setPadding(bars.left, bars.top, bars.right, bars.bottom);
            } else {
                view.setPadding(insets.getSystemWindowInsetLeft(), insets.getSystemWindowInsetTop(),
                        insets.getSystemWindowInsetRight(), insets.getSystemWindowInsetBottom());
            }
            return insets;
        });
        setContentView(scroll);
        label(content, "BufferSync", 30, true);
        label(content, "Текст с телефона — на компьютер", 16, false);
        label(content, "1. Подключение", 21, true);
        label(content, "Запусти программу на компьютере и вставь её ссылку. Оба устройства должны быть в одной Wi-Fi сети.", 14, false);
        link = new EditText(this);
        link.setHint("buffersync://…");
        link.setSingleLine(true);
        link.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_VISIBLE_PASSWORD);
        link.setFilters(new InputFilter[]{new InputFilter.LengthFilter(1024)});
        content.addView(link);
        connect = button(content, "Подключиться", this::toggleConnection);
        connectionStatus = label(content, "Не подключено", 14, false);
        automatic = new CheckBox(this);
        automatic.setText("Автоотправка при открытии приложения");
        content.addView(automatic);
        automatic.setOnCheckedChangeListener((v, checked) -> { if (checked) inspectClipboard(); });
        copyReceived = new CheckBox(this);
        copyReceived.setText("Копировать полученное в буфер телефона");
        copyReceived.setChecked(true);
        content.addView(copyReceived);
        label(content, "Android разрешает читать буфер, когда это окно активно. Из другого приложения можно выбрать «Поделиться → BufferSync».", 13, false);
        label(content, "2. Текст", 21, true);
        editor = new EditText(this);
        editor.setHint("Вставь или напиши текст…");
        editor.setGravity(android.view.Gravity.TOP);
        editor.setMinLines(4);
        editor.setMaxLines(9);
        editor.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_FLAG_MULTI_LINE | InputType.TYPE_TEXT_FLAG_CAP_SENTENCES);
        editor.setFilters(new InputFilter[]{new InputFilter.LengthFilter(SyncClient.MAX_BYTES)});
        content.addView(editor);
        button(content, "Вставить из буфера", () -> {
            String text = readClipboard();
            if (text == null || text.isEmpty()) { tell("В буфере нет доступного текста."); return; }
            if (!SyncClient.valid(text)) { tell("Нужен текст до 64 КБ. Ссылку подключения отправлять нельзя."); return; }
            editor.setText(text); remember(text);
        });
        button(content, "Отправить на устройства", () -> send(editor.getText().toString()));
        button(content, "Скопировать текст", () -> {
            String text = editor.getText().toString();
            if (!SyncClient.valid(text)) { tell("Нужен непустой текст до 64 КБ."); return; }
            putClipboard(text); remember(text); tell("Текст скопирован.");
        });
        message = label(content, "Можно пользоваться своим буфером и без подключения.", 14, false);
        label(content, "История этого сеанса", 21, true);
        label(content, "Нажми на запись, чтобы открыть её в поле текста. Храним до 10 записей, только пока открыт этот экран.", 13, false);
        history = new LinearLayout(this);
        history.setOrientation(LinearLayout.VERTICAL);
        content.addView(history);
        button(content, "Очистить историю", () -> { items.clear(); history.removeAllViews(); });
        if (state != null) {
            link.setText(state.getString("link", ""));
            editor.setText(state.getString("editor", ""));
            automatic.setChecked(state.getBoolean("automatic", false));
            copyReceived.setChecked(state.getBoolean("copyReceived", true));
        }
        acceptShare(getIntent());
    }

    private void toggleConnection() {
        if (client != null) { disconnect(); return; }
        try {
            Pairing pairing = Pairing.parse(link.getText().toString());
            SyncClient next = new SyncClient(pairing, new SyncClient.Listener() {
                // Each callback is checked against its own listener's client generation.
                final int generation = ++connectionGeneration;
                private void ui(Runnable action) {
                    runOnUiThread(() -> { if (generation == connectionGeneration && client != null) action.run(); });
                }
                public void connected() { ui(() -> {
                    connected = true;
                    connectionStatus.setText("Подключено к " + pairing.host);
                    inspectClipboard();
                }); }
                public void received(String text) { ui(() -> {
                    remember(text);
                    tell("Получен текст с подключённого устройства.");
                    if (copyReceived.isChecked()) {
                        if (resumed && hasWindowFocus()) putClipboard(text);
                        else pending = text;
                    }
                }); }
                public void sent() { ui(() -> {
                    tell("Отправлено: компьютер подтвердил приём.");
                    if (pendingOutgoing != null) {
                        String text = pendingOutgoing; pendingOutgoing = null; send(text);
                    }
                }); }
                public void failed(String error) { ui(() -> { disconnect(); tell(error); }); }
            });
            client = next;
            connect.setText("Отключиться");
            link.setEnabled(false);
            connectionStatus.setText("Подключаемся…");
            next.start();
        } catch (Exception e) {
            tell(e instanceof IllegalArgumentException ? e.getMessage() : "Не удалось настроить подключение.");
        }
    }
    private int connectionGeneration;
    private void disconnect() {
        connectionGeneration++;
        if (client != null) client.close();
        client = null; connected = false; pending = null; pendingOutgoing = null;
        connect.setText("Подключиться");
        link.setEnabled(true);
        connectionStatus.setText("Не подключено");
    }
    private void send(String text) {
        if (!SyncClient.valid(text)) { tell("Нужен текст до 64 КБ. Ссылку подключения отправлять нельзя."); return; }
        remember(text);
        if (!connected || client == null) { tell("Текст сохранён в истории. Для отправки подключись к компьютеру."); return; }
        if (client.send(text)) { lastObserved = text; tell("Отправляем…"); }
        else { pendingOutgoing = text; tell("Текст в очереди на отправку."); }
    }
    private String readClipboard() {
        if (!resumed || !hasWindowFocus()) return null;
        try {
            ClipData clip = clipboard.getPrimaryClip();
            if (clip == null || clip.getItemCount() == 0) return null;
            CharSequence text = clip.getItemAt(0).getText();
            return text == null ? null : text.toString();
        } catch (RuntimeException e) { return null; }
    }
    private void inspectClipboard() {
        if (!resumed || !hasWindowFocus() || !connected || !automatic.isChecked()) return;
        String text = readClipboard();
        if (SyncClient.valid(text) && !text.equals(lastObserved)) {
            // If the user copied something while away, keep that newer local action.
            pending = null;
            send(text);
        }
    }
    private void putClipboard(String text) {
        try {
            lastObserved = text; // Mark before the event, so incoming text does not bounce back.
            clipboard.setPrimaryClip(ClipData.newPlainText("BufferSync", text));
        } catch (RuntimeException e) { tell("Не удалось скопировать. Текст остался в истории."); }
    }
    private void remember(String text) {
        if (!SyncClient.valid(text)) return;
        items.remove(text); items.add(0, text);
        if (items.size() > 10) items.remove(items.size()-1);
        history.removeAllViews();
        for (String item : items) {
            Button entry = button(history, item.length() > 90 ? item.substring(0,90) + "…" : item,
                    () -> editor.setText(item));
            entry.setAllCaps(false);
            entry.setMaxLines(2);
            entry.setGravity(android.view.Gravity.START | android.view.Gravity.CENTER_VERTICAL);
        }
    }
    private void acceptShare(Intent intent) {
        if (intent != null && Intent.ACTION_SEND.equals(intent.getAction()) && "text/plain".equals(intent.getType())) {
            CharSequence text = intent.getCharSequenceExtra(Intent.EXTRA_TEXT);
            if (text != null && SyncClient.valid(text.toString())) {
                editor.setText(text); remember(text.toString());
                tell("Текст готов. Нажми «Отправить на устройства».");
            } else tell("Можно принять только текст до 64 КБ.");
        }
    }
    @Override protected void onNewIntent(Intent intent) { super.onNewIntent(intent); setIntent(intent); acceptShare(intent); }
    @Override protected void onResume() {
        super.onResume(); resumed = true;
        clipboard.addPrimaryClipChangedListener(clipboardListener);
    }
    @Override public void onWindowFocusChanged(boolean focused) {
        super.onWindowFocusChanged(focused);
        if (focused && editor != null) {
            inspectClipboard();
            if (pending != null) {
                String text = pending; pending = null;
                if (copyReceived.isChecked()) putClipboard(text);
            }
        }
    }
    @Override protected void onPause() {
        resumed = false; clipboard.removePrimaryClipChangedListener(clipboardListener);
        super.onPause();
    }
    @Override protected void onSaveInstanceState(Bundle out) {
        super.onSaveInstanceState(out);
        out.putString("link", link.getText().toString());
        out.putString("editor", editor.getText().toString());
        out.putBoolean("automatic", automatic.isChecked());
        out.putBoolean("copyReceived", copyReceived.isChecked());
    }
    @Override protected void onDestroy() { disconnect(); super.onDestroy(); }
    private void tell(String text) { if (message != null) message.setText(text); }
    private int dp(int value) { return Math.round(value * getResources().getDisplayMetrics().density); }
    private TextView label(LinearLayout parent, String text, int size, boolean bold) {
        TextView view = new TextView(this);
        view.setText(text); view.setTextSize(size); view.setTextColor(Color.rgb(24,39,65));
        if (bold) view.setTypeface(null, android.graphics.Typeface.BOLD);
        view.setPadding(0, dp(bold ? 18 : 7), 0, dp(7));
        parent.addView(view); return view;
    }
    private Button button(LinearLayout parent, String text, Runnable action) {
        Button view = new Button(this); view.setText(text); view.setAllCaps(false);
        parent.addView(view, new LinearLayout.LayoutParams(-1, -2));
        view.setOnClickListener(v -> action.run()); return view;
    }
}
