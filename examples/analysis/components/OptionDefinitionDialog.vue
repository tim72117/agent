<template>
    <div class="text-center">
        <v-dialog width="auto">
            <template v-slot:activator="{ props }">
                <v-btn v-bind="props" variant="text">
                    <span class="mx-1">選項定義</span>
                </v-btn>
            </template>

            <v-card width="800">
                <v-toolbar color="primary">
                    <v-toolbar-title :text="question.title"></v-toolbar-title>
                </v-toolbar>
                <v-card-text>
                    <v-card color="grey-lighten-2" variant="outlined">
                        <v-table>
                            <thead>
                                <tr>
                                    <th style="border-left: none">數值</th>
                                    <th>選項名稱</th>
                                </tr>
                            </thead>
                            <tbody>
                                <tr v-for="(answer, idx) in question.answers" :key="idx">
                                    <td>{{ answer.value }}</td>
                                    <td>{{ answer.title }}</td>
                                </tr>
                            </tbody>
                        </v-table>
                    </v-card>
                </v-card-text>
            </v-card>
        </v-dialog>
    </div>
</template>

<script>
export default {
    props: {
        question: { type: Object, default: () => ({}) },
    },
}
</script>

<style scoped>
:deep(.v-toolbar__content) {
    padding-top: 20px;
    padding-bottom: 20px;
    height: auto !important;
}
:deep(.v-toolbar-title__placeholder) {
    text-overflow: clip !important;
    white-space: normal;
}
</style>
