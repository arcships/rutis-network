pluginManagement {
    repositories { google(); gradlePluginPortal(); mavenCentral() }
    plugins { kotlin("android") version "2.2.21" }
}
dependencyResolutionManagement { repositories { mavenCentral(); google() } }
rootProject.name = "rutis-rcp"
if (providers.gradleProperty("android").orNull == "true") include(":android")
