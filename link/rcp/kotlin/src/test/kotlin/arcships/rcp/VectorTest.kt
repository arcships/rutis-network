package arcships.rcp

import java.io.File
import kotlin.test.Test

class VectorTest {
    @Test fun goWireVector() = checkGoVector(File("../testdata/wire-v1.json").readText())
}
