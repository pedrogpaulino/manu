package example.hostile;

// class FakeComment { void fake() {} }
public class Hostile {
    String source = "class FakeString { void fake() {} }";
    String annotation = "@Fake(value = \"not-a-real-annotation\")";
    /* interface FakeBlock { void fake(); } */
    void real() {}
}
