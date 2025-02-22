function main(){
    if (isDecimalNumber(allParams.addNumber)) {
        let result = parseFloat(2) + parseFloat(allParams.addNumber);
        return JSON.stringify({
            "success": true,
            "status": 200,
            "data": {
                "result": result
            },
        });
    } else {
        return JSON.stringify({
            "success": false,
            "status": 500,
            "error": {
                "message": "addNumberは必須項目で、数値である必要があります。"
            },
        });
    }
}

function isDecimalNumber(value) {
    if (typeof value !== "string") return false; // 文字列でなければ false

    // 数字のみ（先頭の `0` を除外しない）で、小数点が1つまで
    if (!/^\d+(\.\d+)?$/.test(value)) return false;

    // 数値変換してチェック
    let num = Number(value);
    return Number.isFinite(num) && !isNaN(num);
}


main();