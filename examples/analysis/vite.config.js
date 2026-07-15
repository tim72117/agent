import path from 'path'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import laravel from 'laravel-vite-plugin'
import multiHtmlPlugin from './multiHtmlPlugin.js'
import devMockPlugin from './devMockPlugin.js'

export const entries = {
    'bandset': 'resources/js/bandset.js',
}

export default defineConfig({
    plugins: [
        devMockPlugin,
        laravel({
            input: [
                'resources/js/analysis.js',
                'resources/js/editor.js',
                'resources/js/selectfeature.js',
                'resources/js/bandset.js',
            ],
            refresh: true,
        }),
        multiHtmlPlugin(entries),
        vue({
            template: {
                transformAssetUrls: {
                    base: null,
                    includeAbsolute: false,
                },
            },
        }),
    ],
    resolve: {
        alias: {
            'vue': 'vue/dist/vue.esm-bundler.js',
            '@': path.resolve(process.cwd(), 'resources/js'),
        },
    },
    server: {
        host: '0.0.0.0',
        port: 5174,
        strictPort: true,
    },
    preview: {
        host: '0.0.0.0',
        port: 5174,
    },
    build: {
        outDir: 'public/js/build',
    },
    base: '/packages/cere/analysis/js/build',
})
