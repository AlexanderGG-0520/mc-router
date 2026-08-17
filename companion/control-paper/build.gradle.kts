dependencies {
    compileOnly("io.papermc.paper:paper-api:1.21.8-R0.1-SNAPSHOT")
    testImplementation("org.junit.jupiter:junit-jupiter:6.0.1")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher")
}

tasks.jar {
    archiveBaseName.set("mc-router-control-companion-paper")
}

val companionVersion = version

tasks.processResources {
    filesMatching("paper-plugin.yml") {
        expand("version" to companionVersion)
    }
}
