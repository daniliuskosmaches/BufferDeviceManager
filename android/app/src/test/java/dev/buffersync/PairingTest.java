package dev.buffersync;

import org.junit.Test;
import static org.junit.Assert.*;

public class PairingTest {
    private final String key = "a".repeat(64), pin = "b".repeat(64);
    @Test public void parsesValidLink() {
        Pairing p = Pairing.parse("buffersync://192.168.1.5:7778?key="+key+"&pin="+pin);
        assertEquals("192.168.1.5",p.host); assertEquals(7778,p.port); assertEquals(key,p.key);
    }
    @Test public void rejectsMalformedAndAmbiguousLinks() {
        String[] bad = {"", "https://host:7778?key="+key+"&pin="+pin,
            "buffersync://host:0?key="+key+"&pin="+pin,
            "buffersync://host:7778?key="+key+"&key="+key+"&pin="+pin,
            "buffersync://host:7778?key=short&pin="+pin,
            "buffersync://user@host:7778?key="+key+"&pin="+pin,
            "buffersync://host:7778/path?key="+key+"&pin="+pin};
        for(String link:bad) {
            try { Pairing.parse(link); fail("accepted: "+link); }
            catch(IllegalArgumentException expected) { }
        }
    }
    @Test public void limitsTextByUtf8BytesAndProtectsPairingLinks() {
        assertTrue(SyncClient.valid("Привет 👋\nline 2"));
        assertTrue(SyncClient.valid("x".repeat(65536)));
        assertFalse(SyncClient.valid("я".repeat(32769)));
        assertFalse(SyncClient.valid("a\0b"));
        assertFalse(SyncClient.valid(""));
        assertFalse(SyncClient.valid("buffersync://secret"));
    }
}
