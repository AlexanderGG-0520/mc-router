import groovy.json.JsonSlurper
import java.net.URI
import java.net.URLEncoder
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import java.nio.file.StandardCopyOption

plugins {
    `java-library`
}

allprojects {
    group = "com.alecofc.mcrouter"
    version = "0.2.0"
}

subprojects {
    apply(plugin = "java-library")

    java {
        toolchain {
            languageVersion.set(JavaLanguageVersion.of(21))
        }
    }

    tasks.withType<JavaCompile>().configureEach {
        options.encoding = "UTF-8"
        options.release.set(21)
    }

    tasks.withType<Test>().configureEach {
        useJUnitPlatform()
    }
}

data class ModrinthDependency(
    val project: String,
    val loader: String,
    val minecraftVersion: String,
    val outputFile: String,
)

val e2eDependencies = listOf(
    ModrinthDependency(
        project = "simple-voice-chat",
        loader = "paper",
        minecraftVersion = "1.21.8",
        outputFile = "simple-voice-chat-paper.jar",
    ),
    ModrinthDependency(
        project = "simple-voice-chat",
        loader = "fabric",
        minecraftVersion = "26.1.2",
        outputFile = "simple-voice-chat-fabric.jar",
    ),
    ModrinthDependency(
        project = "fabric-api",
        loader = "fabric",
        minecraftVersion = "26.1.2",
        outputFile = "fabric-api.jar",
    ),
)

tasks.register("downloadVoicechatE2eDependencies") {
    group = "voicechat"
    description = "Downloads official Simple Voice Chat and Fabric API JARs for the local E2E environment."

    val outputDirectory = rootProject.projectDir.parentFile.resolve("local-jars")
    outputs.dir(outputDirectory)

    doLast {
        outputDirectory.mkdirs()

        for (dependency in e2eDependencies) {
            val loaders = """["${dependency.loader}"]"""
            val gameVersions = """["${dependency.minecraftVersion}"]"""

            val query = listOf(
                "loaders" to loaders,
                "game_versions" to gameVersions,
            ).joinToString("&") { (key, value) ->
                "$key=${URLEncoder.encode(value, StandardCharsets.UTF_8)}"
            }

            val versionsUri = URI.create(
                "https://api.modrinth.com/v2/project/${dependency.project}/version?$query"
            )

            val connection = versionsUri.toURL().openConnection().apply {
                setRequestProperty(
                    "User-Agent",
                    "AlexanderGG-0520/mc-router voicechat E2E dependency resolver",
                )
                connectTimeout = 15_000
                readTimeout = 30_000
            }

            val versions = connection.getInputStream().bufferedReader().use {
                @Suppress("UNCHECKED_CAST")
                JsonSlurper().parse(it) as List<Map<String, Any?>>
            }

            val selectedVersion = versions.firstOrNull {
                it["version_type"] == "release"
            } ?: versions.firstOrNull()
                ?: error(
                    "No Modrinth version found for " +
                        "${dependency.project}, loader=${dependency.loader}, " +
                        "Minecraft=${dependency.minecraftVersion}"
                )

            @Suppress("UNCHECKED_CAST")
            val files = selectedVersion["files"] as? List<Map<String, Any?>>
                ?: error("Modrinth response contains no files for ${dependency.project}")

            val selectedFile = files.firstOrNull {
                it["primary"] == true
            } ?: files.firstOrNull()
                ?: error("Modrinth version contains no downloadable files for ${dependency.project}")

            val downloadUrl = selectedFile["url"] as? String
                ?: error("Modrinth file contains no download URL for ${dependency.project}")

            val destination = outputDirectory.resolve(dependency.outputFile)
            val temporary = outputDirectory.resolve("${dependency.outputFile}.tmp")

            logger.lifecycle(
                "Downloading {} ({}, Minecraft {}) to {}",
                dependency.project,
                dependency.loader,
                dependency.minecraftVersion,
                destination,
            )

            val downloadConnection = URI.create(downloadUrl).toURL().openConnection().apply {
                setRequestProperty(
                    "User-Agent",
                    "AlexanderGG-0520/mc-router voicechat E2E dependency resolver",
                )
                connectTimeout = 15_000
                readTimeout = 60_000
            }

            downloadConnection.getInputStream().use { input ->
                Files.copy(
                    input,
                    temporary.toPath(),
                    StandardCopyOption.REPLACE_EXISTING,
                )
            }

            Files.move(
                temporary.toPath(),
                destination.toPath(),
                StandardCopyOption.REPLACE_EXISTING,
                StandardCopyOption.ATOMIC_MOVE,
            )

            if (!destination.isFile || destination.length() == 0L) {
                error("Downloaded dependency is empty or missing: $destination")
            }
        }
    }
}
