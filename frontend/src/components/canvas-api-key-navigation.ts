export function canvasAPIKeyManagementPath(openCreate = false): string {
    return openCreate ? "/studio/credentials?create=1" : "/studio/credentials";
}
