package arcships.rcp

import androidx.test.platform.app.InstrumentationRegistry
import org.junit.Test

class AndroidVectorTest {
    @Test fun goWireVector() {
        val assets = InstrumentationRegistry.getInstrumentation().context.assets
        assets.open("wire-v1.json").bufferedReader().use { checkGoVector(it.readText()) }
    }
}
